import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import os from 'node:os'
import crypto from 'node:crypto'
import { once } from 'node:events'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { execFileSync } from 'node:child_process'

const native = process.env['PAIR_RUN_CODEX_E2E'] === '1'

class JsonLineProcess {
    private buffer = ''
    private stderr = ''
    private readonly waiting: Array<{
        predicate: (value: Record<string, unknown>) => boolean
        resolve: (value: Record<string, unknown>) => void
        reject: (error: Error) => void
        timer: NodeJS.Timeout
    }> = []

    constructor(private readonly process: ChildProcessWithoutNullStreams) {
        process.stderr.on('data', chunk => {
            this.stderr += chunk.toString('utf8')
            if (this.stderr.length > 4000) this.stderr = this.stderr.slice(-4000)
        })
        process.stdout.on('data', chunk => {
            this.buffer += chunk.toString('utf8')
            for (;;) {
                const newline = this.buffer.indexOf('\n')
                if (newline < 0) return
                const line = this.buffer.slice(0, newline)
                this.buffer = this.buffer.slice(newline + 1)
                try {
                    const value = JSON.parse(line) as unknown
                    if (!value || typeof value !== 'object' || Array.isArray(value)) continue
                    this.deliver(value as Record<string, unknown>)
                } catch {
                    // A malformed child line is ignored; the timeout reports the
                    // missing lifecycle/MCP response to the test.
                }
            }
        })
    }

    next(predicate: (value: Record<string, unknown>) => boolean, timeoutMs = 15_000, label = 'native Codex process response'): Promise<Record<string, unknown>> {
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                const index = this.waiting.findIndex(item => item.timer === timer)
                if (index >= 0) this.waiting.splice(index, 1)
                reject(new Error(`timed out waiting for ${label}; stderr=${this.stderr}`))
            }, timeoutMs)
            this.waiting.push({ predicate, resolve, reject, timer })
        })
    }

    stderrText(): string {
        return this.stderr
    }

    private deliver(value: Record<string, unknown>): void {
        const index = this.waiting.findIndex(item => item.predicate(value))
        if (index < 0) return
        const item = this.waiting.splice(index, 1)[0]
        clearTimeout(item.timer)
        item.resolve(value)
    }
}

function writeJsonLine(process: ChildProcessWithoutNullStreams, value: Record<string, unknown>): void {
    process.stdin.write(`${JSON.stringify(value)}\n`)
}

function toolText(response: Record<string, unknown>): string {
    const result = response.result as { content?: Array<{ text?: unknown }> } | undefined
    const text = result?.content?.[0]?.text
    if (typeof text !== 'string') throw new Error(`native MCP response has no text content: ${JSON.stringify(response)}`)
    return text
}

function executablePath(directory: string, baseName: string): string {
    return path.join(directory, `${baseName}${process.platform === 'win32' ? '.exe' : ''}`)
}

describe('packaged Codex Desktop native contract', () => {
    it.skipIf(!native)('contains both packaged Codex binaries and a manifest', () => {
        const cliBin = process.env['PAIR_CODEX_E2E_CLI_BIN']
        if (!cliBin) throw new Error('PAIR_CODEX_E2E_CLI_BIN is required for native Codex E2E')
        const manifestPath = path.join(cliBin, 'manifest.json')
        const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8')) as {
            files?: Array<{ fileName?: string }>
        }
        const names = new Set((manifest.files ?? []).map(file => file.fileName))
        const suffix = process.platform === 'win32' ? '.exe' : ''
        expect(names.has(`nvpair-codex-worker${suffix}`)).toBe(true)
        expect(names.has(`nvpair-codex-supervisor${suffix}`)).toBe(true)
    })

    it.skipIf(!native)('starts the native Worker and Supervisor MCP path', async () => {
        const cliBin = process.env['PAIR_CODEX_E2E_CLI_BIN']
        if (!cliBin) throw new Error('PAIR_CODEX_E2E_CLI_BIN is required for native Codex E2E')
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-codex-native-e2e-'))
        const workspace = path.join(root, 'workspace')
        const state = path.join(root, 'worker-state')
        const supervisorState = path.join(root, 'supervisor-state')
        const management = path.join(root, 'management')
        const runtime = path.join(root, 'runtime.json')
        const credential = path.join(root, 'worker-credential.json')
        fs.mkdirSync(workspace, { recursive: true, mode: 0o700 })
        execFileSync('git', ['init', '--quiet', workspace])
        const workerConfig = path.join(root, 'worker-config.json')
        fs.writeFileSync(
            workerConfig,
            `${JSON.stringify(
                {
                    schemaVersion: 1,
                    installationId: crypto.randomUUID(),
                    workerInstanceId: crypto.randomUUID(),
                    bootEpoch: Date.now(),
                    generation: 1,
                    workspaceRoot: workspace,
                    stateRoot: state,
                    account: os.userInfo().username,
                    codexBin: process.env['PAIR_CODEX_BIN'] || 'codex',
                    maxConcurrency: 1,
                    authToken: crypto.randomBytes(32).toString('hex'),
                    runtimeDescriptorPath: runtime,
                    credentialRef: credential,
                    credentialGeneration: 1,
                    policyRevision: 1,
                    artifactMaxBytes: 8 << 20,
                    policyCeiling: 'read-only',
                    workspaceAlias: 'local',
                    toolLabels: []
                },
                null,
                2
            )}\n`,
            { mode: 0o600 }
        )

        let worker: ChildProcessWithoutNullStreams | null = null
        let workerLines: JsonLineProcess | null = null
        let supervisor: ChildProcessWithoutNullStreams | null = null
        try {
            worker = spawn(executablePath(cliBin, 'nvpair-codex-worker'), ['--managed-control'], {
                stdio: 'pipe'
            })
            const managedWorkerLines = new JsonLineProcess(worker)
            workerLines = managedWorkerLines
            writeJsonLine(worker, {
                schemaVersion: 1,
                kind: 'start',
                requestId: 'native-e2e-start',
                configRevision: 1,
                configPath: workerConfig,
                workspaceAlias: 'local'
            })
            const ready = await managedWorkerLines.next(value => value.kind === 'ready', 15_000, 'managed Worker ready')
            expect((ready.descriptor as Record<string, unknown>).state).toBe('ready')

            supervisor = spawn(executablePath(cliBin, 'nvpair-codex-supervisor'), [
                '--runtime-descriptor',
                runtime,
                '--state-root',
                supervisorState,
                '--management-socket-dir',
                management
            ], { stdio: 'pipe' })
            const supervisorLines = new JsonLineProcess(supervisor)
            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 1,
                method: 'initialize',
                params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'native-e2e', version: '1' } }
            })
            const initialized = await supervisorLines.next(value => value.id === 1, 15_000, 'Supervisor initialize')
            expect(initialized.error).toBeUndefined()
            writeJsonLine(supervisor, { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} })
            const tools = await supervisorLines.next(value => value.id === 2, 15_000, 'Supervisor tools/list')
            expect(JSON.stringify(tools.result)).toContain('workers.list')
            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 3,
                method: 'tools/call',
                params: { name: 'workers.list', arguments: {} }
            })
            const workers = await supervisorLines.next(value => value.id === 3, 15_000, 'Supervisor workers.list')
            expect(JSON.stringify(workers.result)).toContain('worker')

            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 4,
                method: 'tools/call',
                params: {
                    name: 'tasks.delegate',
                    arguments: {
                        objective: 'Inspect the repository status and report whether the working tree is clean. Do not modify any files.',
                        workspace: 'local',
                        mode: 'read',
                        approval: 'local-only'
                    }
                }
            })
            const delegated = await supervisorLines.next(value => value.id === 4, 90_000, 'native read-only task delegation')
            const delegatedResult = delegated.result as { isError?: boolean } | undefined
            expect(delegated.error).toBeUndefined()
            expect(delegatedResult?.isError).not.toBe(true)
            const delegatedPayload = JSON.parse(toolText(delegated)) as {
                record?: { taskId?: string; attemptId?: string; leaseEpoch?: number }
            }
            const task = delegatedPayload.record
            if (!task?.taskId || !task.attemptId || !task.leaseEpoch) {
                throw new Error(`native task delegation returned no fenced record: ${toolText(delegated)}`)
            }

            let terminalState = ''
            for (let attempt = 0; attempt < 60; attempt++) {
                const id = 100 + attempt
                writeJsonLine(supervisor, {
                    jsonrpc: '2.0',
                    id,
                    method: 'tools/call',
                    params: { name: 'tasks.status', arguments: { taskId: task.taskId } }
                })
                const statusResponse = await supervisorLines.next(value => value.id === id, 15_000, `native task status ${attempt + 1}`)
                const statusPayload = JSON.parse(toolText(statusResponse)) as { state?: string }
                terminalState = statusPayload.state ?? ''
                if (['completed', 'blocked', 'failed', 'cancelled', 'lost'].includes(terminalState)) break
                await new Promise(resolve => setTimeout(resolve, 1_000))
            }
            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 200,
                method: 'tools/call',
                params: { name: 'tasks.result', arguments: { taskId: task.taskId } }
            })
            const taskResult = await supervisorLines.next(value => value.id === 200, 15_000, 'native read-only task result')
            if (terminalState !== 'completed') {
                throw new Error(`native read-only task ended in ${terminalState}: ${toolText(taskResult)}; worker stderr=${workerLines?.stderrText() ?? ''}`)
            }
            expect(terminalState).toBe('completed')
            expect(JSON.stringify(taskResult.result)).toContain(task.taskId)
        } finally {
            if (supervisor) {
                supervisor.stdin.end()
                await once(supervisor, 'exit').catch(() => undefined)
            }
            if (worker) {
                writeJsonLine(worker, {
                    schemaVersion: 1,
                    kind: 'shutdown',
                    requestId: 'native-e2e-shutdown'
                })
                worker.stdin.end()
                await once(worker, 'exit').catch(() => undefined)
            }
            fs.rmSync(root, { recursive: true, force: true })
        }
    })
})
