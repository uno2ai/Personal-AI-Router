// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Explicit opt-in live check. Drops only this test's TCP forwarding connections;
// mTLS remains end-to-end between the Supervisor and the paired remote Worker.
import net from 'node:net'
import { spawn } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import readline from 'node:readline'
import assert from 'node:assert/strict'

const [binary, clusterDir, principal, host, portText] = process.argv.slice(2)
if (!binary || !clusterDir || !principal || !net.isIP(host) || !Number(portText)) {
    throw new Error('Usage: node verify-codex-remote-reconnect.mjs SUPERVISOR CLUSTER_DIR PRINCIPAL IP PORT')
}
const sockets = new Set()
let connected = true
const proxy = net.createServer(client => {
    if (!connected) { client.destroy(); return }
    const upstream = net.connect(Number(portText), host)
    for (const socket of [client, upstream]) {
        sockets.add(socket)
        socket.on('close', () => sockets.delete(socket))
        socket.on('error', () => { client.destroy(); upstream.destroy() })
    }
    client.on('close', () => upstream.destroy())
    upstream.on('close', () => client.destroy())
    client.pipe(upstream).pipe(client)
})
await new Promise((resolve, reject) => {
    proxy.once('error', reject)
    proxy.listen(0, '127.0.0.1', resolve)
})
const state = mkdtempSync(path.join(os.tmpdir(), 'pair-reconnect-'))
const child = spawn(binary, ['--cluster-dir', clusterDir, '--worker-endpoints',
    `${principal}=https://127.0.0.1:${proxy.address().port}`, '--state-root', state],
    { stdio: ['pipe', 'pipe', 'pipe'] })
const pending = new Map()
let sequence = 0
let taskId
let attemptId
let leaseEpoch
let terminal = false
child.on('error', error => { for (const item of pending.values()) item.reject(error) })
child.on('exit', code => { for (const item of pending.values()) item.reject(new Error(`Supervisor exited ${code}`)) })
child.stderr.resume()
readline.createInterface({ input: child.stdout }).on('line', line => {
    const response = JSON.parse(line)
    const item = pending.get(response.id)
    if (item) { pending.delete(response.id); item.resolve(response) }
})
function rpc(method, params) {
    return new Promise((resolve, reject) => {
        const id = ++sequence
        const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`RPC ${method} timed out`)) }, 20000)
        pending.set(id, {
            resolve: value => { clearTimeout(timeout); resolve(value) },
            reject: error => { clearTimeout(timeout); reject(error) }
        })
        child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`)
    })
}
async function tool(name, args = {}) {
    const response = await rpc('tools/call', { name, arguments: args })
    if (response.error || response.result?.isError) throw new Error(`${name} failed`)
    return response.result.structuredContent ?? JSON.parse(response.result.content[0].text)
}
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
try {
    await rpc('initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'pair-reconnect-check', version: '1' } })
    const delegated = await tool('tasks.delegate', {
        objective: 'Read-only connection recovery test. Run PowerShell Start-Sleep -Seconds 20 without modifying any files, then report PAIR_RECONNECT_OK.',
        workspace: 'local', mode: 'read', workerId: principal
    })
    ;({ taskId, attemptId, leaseEpoch } = delegated.record)
    let running
    for (let i = 0; i < 20; i++) {
        running = await tool('tasks.status', { taskId })
        if (running.state === 'running' && running.childIdentity) break
        assert(!['failed', 'blocked', 'cancelled', 'completed', 'lost'].includes(running.state), 'Task ended before interruption')
        await delay(500)
    }
    assert.equal(running.state, 'running')
    connected = false
    for (const socket of sockets) socket.destroy()
    let unavailable = false
    try { await tool('tasks.status', { taskId }) } catch { unavailable = true }
    assert(unavailable, 'Status must report connection failure during interruption')
    await delay(3000)
    connected = true
    let restored
    for (let i = 0; i < 90; i++) {
        restored = await tool('tasks.status', { taskId })
        assert.equal(restored.attemptId, attemptId)
        assert.equal(restored.childIdentity, running.childIdentity)
        if (['completed', 'failed', 'blocked', 'cancelled', 'lost'].includes(restored.state)) break
        await delay(1000)
    }
    terminal = ['completed', 'failed', 'blocked', 'cancelled', 'lost'].includes(restored.state)
    assert.equal(restored.state, 'completed')
    console.log(JSON.stringify({ result: 'PASS', connectionFailureObserved: unavailable,
        sameAttempt: restored.attemptId === attemptId, sameChild: restored.childIdentity === running.childIdentity,
        finalState: restored.state }))
} finally {
    connected = true
    if (taskId && !terminal) {
        try { await tool('tasks.cancel', { taskId, attemptId, leaseEpoch }) } catch { /* keep original diagnostic */ }
    }
    child.stdin.end()
    child.kill('SIGTERM')
    for (const socket of sockets) socket.destroy()
    proxy.close()
}
