// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { expect, it } from 'vitest'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { JsonLineProcess, stopProcess, parseNativeObject } from '@tests/fixtures/native-process'

it('keeps malformed response bytes out of parser errors', () => {
    expect(() => parseNativeObject('private-fixture')).toThrow('invalid JSON')
    expect(() => parseNativeObject('private-fixture')).not.toThrow('private-fixture')
    expect(() => parseNativeObject('[]')).toThrow('must be an object')
})

it('retains a lifecycle event arriving before its waiter', async () => {
    const child = spawn(process.execPath, ['-e', 'console.log(JSON.stringify({kind:"ready"}))'], {
        stdio: 'pipe'
    })
    const lines = new JsonLineProcess(child)
    await once(child, 'close')
    expect((await lines.next(value => value.kind === 'ready', 100)).kind).toBe('ready')
})

it('rejects promptly on exit and excludes stderr content from diagnostics', async () => {
    const child = spawn(
        process.execPath,
        ['-e', 'process.stderr.write("private-fixture"); process.exit(7)'],
        { stdio: 'pipe' }
    )
    const lines = new JsonLineProcess(child)
    await once(child, 'close')
    await expect(lines.next(() => true, 100)).rejects.toThrow(/exit=7/)
    await expect(lines.next(() => true, 100)).rejects.not.toThrow('private-fixture')
})

it('reports spawn errors without exposing the attempted executable path', async () => {
    const child = spawn('missing-private-fixture-executable', [], { stdio: 'pipe' })
    const lines = new JsonLineProcess(child)
    await expect(lines.next(() => true)).rejects.toThrow('could not start')
    await expect(lines.next(() => true)).rejects.not.toThrow('missing-private-fixture-executable')
    await stopProcess(child)
})

it('rejects malformed process output without including its bytes', async () => {
    const child = spawn(process.execPath, ['-e', 'console.log("private-fixture")'], {
        stdio: 'pipe'
    })
    const lines = new JsonLineProcess(child)
    await expect(lines.next(() => true)).rejects.toThrow('invalid JSON')
    await expect(lines.next(() => true)).rejects.not.toThrow('private-fixture')
    await stopProcess(child)
})

it('finishes cleanup for already exited processes and terminates an owned noncooperative process', async () => {
    const child = spawn(
        process.execPath,
        ['-e', 'console.log("ready"); setInterval(() => {}, 1000)'],
        { stdio: 'pipe' }
    )
    await once(child.stdout, 'data')
    await stopProcess(child)
    expect(child.exitCode !== null || child.signalCode !== null).toBe(true)
    await stopProcess(child)
}, 12_000)
