// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import type { JsonObject, JsonValue } from '@/electron/service-bridge/json-rpc-subprocess'

export function parseNativeObject(text: string): JsonObject {
    let value: JsonValue
    try {
        value = JSON.parse(text)
    } catch {
        throw new Error('native response contains invalid JSON')
    }
    if (!isNativeObject(value)) throw new Error('native response must be an object')
    return value
}

export function isNativeObject(value: JsonValue | undefined): value is JsonObject {
    return value !== null && typeof value === 'object' && !Array.isArray(value)
}

export class JsonLineProcess {
    private buffer = ''
    private stderrBytes = 0
    private failure: Error | null = null
    private readonly queued: JsonObject[] = []
    private readonly waiting: Array<{
        predicate: (value: JsonObject) => boolean
        resolve: (value: JsonObject) => void
        reject: (error: Error) => void
        timer: NodeJS.Timeout
    }> = []

    constructor(private readonly child: ChildProcessWithoutNullStreams) {
        child.stderr.on('data', chunk => {
            this.stderrBytes += chunk.length
        })
        child.on('error', () => this.fail('could not start'))
        child.stdin.on('error', () => this.fail('input closed'))
        child.on('close', code => this.fail(`exited; exit=${code ?? 'signal'}`))
        child.stdout.on('data', chunk => {
            if (this.failure) return
            this.buffer += chunk.toString('utf8')
            if (this.buffer.length > 4 << 20) {
                this.fail('output exceeded 4 MiB')
                return
            }
            for (;;) {
                const newline = this.buffer.indexOf('\n')
                if (newline < 0) return
                const line = this.buffer.slice(0, newline)
                this.buffer = this.buffer.slice(newline + 1)
                let value: JsonValue
                try {
                    value = JSON.parse(line)
                } catch {
                    this.fail('emitted invalid JSON')
                    return
                }
                if (!value || typeof value !== 'object' || Array.isArray(value)) {
                    this.fail('emitted a non-object')
                    return
                }
                if (value.kind === 'error') {
                    this.fail('reported a lifecycle error')
                    return
                }
                this.deliver(value)
            }
        })
    }

    next(
        predicate: (value: JsonObject) => boolean,
        timeoutMs = 15_000,
        label = 'native Codex process response'
    ): Promise<JsonObject> {
        const index = this.queued.findIndex(predicate)
        if (index >= 0) return Promise.resolve(this.queued.splice(index, 1)[0])
        if (this.failure) return Promise.reject(this.failure)
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                const index = this.waiting.findIndex(item => item.timer === timer)
                if (index >= 0) this.waiting.splice(index, 1)
                reject(new Error(`timed out waiting for ${label}; ${this.diagnostics()}`))
            }, timeoutMs)
            this.waiting.push({ predicate, resolve, reject, timer })
        })
    }

    diagnostics(): string {
        return `pid=${this.child.pid ?? 'unavailable'}; exit=${this.child.exitCode ?? 'running'}; stderrBytes=${this.stderrBytes}; queued=${this.queued.length}`
    }

    private fail(reason: string): void {
        if (this.failure) return
        this.failure = new Error(`native process ${reason}; ${this.diagnostics()}`)
        for (const item of this.waiting.splice(0)) {
            clearTimeout(item.timer)
            item.reject(this.failure)
        }
    }

    private deliver(value: JsonObject): void {
        const index = this.waiting.findIndex(item => item.predicate(value))
        if (index < 0) {
            if (this.queued.length >= 256) this.fail('event queue exceeded 256 records')
            else this.queued.push(value)
            return
        }
        const item = this.waiting.splice(index, 1)[0]
        clearTimeout(item.timer)
        item.resolve(value)
    }
}

function waitForExit(child: ChildProcessWithoutNullStreams, timeoutMs: number): Promise<boolean> {
    if (child.exitCode !== null || child.signalCode !== null || !child.pid)
        return Promise.resolve(true)
    return new Promise(resolve => {
        const finish = (exited: boolean): void => {
            clearTimeout(timer)
            child.off('exit', onExit)
            resolve(exited)
        }
        const onExit = (): void => finish(true)
        const timer = setTimeout(() => finish(false), timeoutMs)
        child.once('exit', onExit)
    })
}

// Only acts on the ChildProcess returned by this fixture's own spawn.
export async function stopProcess(child: ChildProcessWithoutNullStreams): Promise<void> {
    child.stdin.on('error', () => undefined)
    if (!child.stdin.destroyed) child.stdin.end()
    if (await waitForExit(child, 5_000)) return
    child.kill('SIGTERM')
    if (await waitForExit(child, 2_000)) return
    child.kill('SIGKILL')
    if (!(await waitForExit(child, 2_000)))
        throw new Error(`owned process cleanup timed out; pid=${child.pid ?? 'unavailable'}`)
}

export async function stopOwnedProcesses(
    children: Array<ChildProcessWithoutNullStreams | null>
): Promise<void> {
    const results = await Promise.allSettled(
        children.map(child => (child ? stopProcess(child) : Promise.resolve()))
    )
    if (results.some(result => result.status === 'rejected'))
        throw new Error('one or more owned processes did not exit within the cleanup deadline')
}
