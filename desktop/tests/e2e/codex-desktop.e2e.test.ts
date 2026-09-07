// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import os from 'node:os'
import crypto from 'node:crypto'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { execFileSync, spawnSync } from 'node:child_process'

import type { JsonObject } from '@/electron/service-bridge/json-rpc-subprocess'
import { runProtectedFile } from '@/electron/protected-file'
import {
    JsonLineProcess,
    stopProcess,
    stopOwnedProcesses,
    parseNativeObject,
    isNativeObject
} from '@tests/fixtures/native-process'

const native = process.env['PAIR_RUN_CODEX_E2E'] === '1'

function writeJsonLine(process: ChildProcessWithoutNullStreams, value: JsonObject): void {
    process.stdin.write(`${JSON.stringify(value)}\n`)
}

function toolText(response: JsonObject): string {
    const result = response.result
    if (!isNativeObject(result) || !Array.isArray(result.content))
        throw new Error('native MCP response has no content')
    const item = result.content[0]
    const text = isNativeObject(item) ? item.text : null
    if (typeof text !== 'string') throw new Error('native MCP response has no text content')
    return text
}

async function waitForLocalWorker(
    process: ChildProcessWithoutNullStreams,
    lines: JsonLineProcess,
    firstID: number,
    timeoutMs = 5_000
): Promise<void> {
    const deadline = Date.now() + timeoutMs
    let id = firstID
    let last = ''
    while (Date.now() < deadline) {
        writeJsonLine(process, {
            jsonrpc: '2.0',
            id,
            method: 'tools/call',
            params: { name: 'workers.list', arguments: {} }
        })
        last = toolText(
            await lines.next(
                value => value.id === id,
                15_000,
                'Supervisor local Worker convergence'
            )
        )
        if (last.includes('"id":"local"')) return
        id++
        await new Promise(resolve => setTimeout(resolve, 100))
    }
    throw new Error(`local Worker did not converge within ${timeoutMs}ms; ${lines.diagnostics()}`)
}

function executablePath(directory: string, baseName: string): string {
    return path.join(directory, `${baseName}${process.platform === 'win32' ? '.exe' : ''}`)
}

interface NativeWorkerFixture {
    workerConfig: string
    runtime: string
    credential: string
    state: string
    workspace: string
    installationId: string
    workerInstanceId: string
}

function createNativeWorkerFixture(
    root: string,
    codexBin: string,
    cliBin: string
): NativeWorkerFixture {
    const workspace = path.join(root, 'workspace')
    const state = path.join(root, 'worker-state')
    const runtime = path.join(root, 'runtime.json')
    const credential = path.join(root, 'worker-credential.json')
    const workerConfig = path.join(root, 'worker-config.json')
    const installationId = crypto.randomUUID()
    const workerInstanceId = crypto.randomUUID()
    fs.mkdirSync(workspace, { recursive: true, mode: 0o700 })
    execFileSync('git', ['init', '--quiet', workspace])
    runProtectedFile(
        executablePath(cliBin, 'nvpair-ui-broker'),
        'write',
        workerConfig,
        `${JSON.stringify(
            {
                schemaVersion: 1,
                installationId,
                workerInstanceId,
                bootEpoch: Date.now(),
                generation: 1,
                workspaceRoot: workspace,
                stateRoot: state,
                account: os.userInfo().username,
                codexBin,
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
        )}\n`
    )
    return { workerConfig, runtime, credential, state, workspace, installationId, workerInstanceId }
}

async function waitForRuntimeDescriptor(
    runtimePath: string,
    minimumBootEpoch = 0,
    timeoutMs = 30_000
): Promise<JsonObject> {
    const deadline = Date.now() + timeoutMs
    let last = ''
    while (Date.now() < deadline) {
        try {
            const descriptor = parseNativeObject(fs.readFileSync(runtimePath, 'utf8'))
            last =
                typeof descriptor.state === 'string' &&
                ['ready', 'starting', 'stopped', 'unavailable'].includes(descriptor.state)
                    ? descriptor.state
                    : 'invalid-state'
            if (
                descriptor.state === 'ready' &&
                typeof descriptor.bootEpoch === 'number' &&
                descriptor.bootEpoch > minimumBootEpoch
            ) {
                return descriptor
            }
        } catch {
            last = 'descriptor-unreadable'
        }
        await new Promise(resolve => setTimeout(resolve, 100))
    }
    throw new Error(`timed out waiting for ready runtime descriptor ${runtimePath}; last=${last}`)
}

describe('packaged Codex Desktop native contract', () => {
    it.skipIf(!native)('contains both packaged Codex binaries and a manifest', () => {
        const cliBin = process.env['PAIR_CODEX_E2E_CLI_BIN']
        if (!cliBin) throw new Error('PAIR_CODEX_E2E_CLI_BIN is required for native Codex E2E')
        const manifestPath = path.join(cliBin, 'manifest.json')
        const manifest = parseNativeObject(fs.readFileSync(manifestPath, 'utf8'))
        const files = Array.isArray(manifest.files) ? manifest.files : []
        const names = new Set(files.filter(isNativeObject).map(file => file.fileName))
        const suffix = process.platform === 'win32' ? '.exe' : ''
        expect(names.has(`nvpair-codex-worker${suffix}`)).toBe(true)
        expect(names.has(`nvpair-codex-supervisor${suffix}`)).toBe(true)
        if (process.platform === 'darwin') {
            const uninstaller = fs.readFileSync(
                path.resolve(cliBin, '../installer-tools/uninstall-macos.sh'),
                'utf8'
            )
            expect(uninstaller).not.toContain('"nvpair-codex-worker"')
            expect(uninstaller).not.toContain('"nvpair-codex-supervisor"')
            expect(uninstaller).toContain('User data preserved')
        }
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
        runProtectedFile(
            executablePath(cliBin, 'nvpair-ui-broker'),
            'write',
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
            )}\n`
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
            const ready = await managedWorkerLines.next(
                value => value.kind === 'ready',
                15_000,
                'managed Worker ready'
            )
            expect(isNativeObject(ready.descriptor) && ready.descriptor.state === 'ready').toBe(
                true
            )

            supervisor = spawn(
                executablePath(cliBin, 'nvpair-codex-supervisor'),
                [
                    '--runtime-descriptor',
                    runtime,
                    '--state-root',
                    supervisorState,
                    '--management-socket-dir',
                    management
                ],
                { stdio: 'pipe' }
            )
            const supervisorLines = new JsonLineProcess(supervisor)
            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 1,
                method: 'initialize',
                params: {
                    protocolVersion: '2025-06-18',
                    capabilities: {},
                    clientInfo: { name: 'native-e2e', version: '1' }
                }
            })
            const initialized = await supervisorLines.next(
                value => value.id === 1,
                15_000,
                'Supervisor initialize'
            )
            expect(initialized.error === undefined).toBe(true)
            writeJsonLine(supervisor, { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} })
            const tools = await supervisorLines.next(
                value => value.id === 2,
                15_000,
                'Supervisor tools/list'
            )
            expect(JSON.stringify(tools.result).includes('workers.list')).toBe(true)
            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 3,
                method: 'tools/call',
                params: { name: 'workers.list', arguments: {} }
            })
            const workers = await supervisorLines.next(
                value => value.id === 3,
                15_000,
                'Supervisor workers.list'
            )
            expect(JSON.stringify(workers.result).includes('worker')).toBe(true)

            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 4,
                method: 'tools/call',
                params: {
                    name: 'tasks.delegate',
                    arguments: {
                        objective:
                            'Inspect the repository status and report whether the working tree is clean. Do not modify any files.',
                        workspace: 'local',
                        mode: 'read',
                        approval: 'local-only'
                    }
                }
            })
            const delegated = await supervisorLines.next(
                value => value.id === 4,
                90_000,
                'native read-only task delegation'
            )
            const delegatedResult = delegated.result
            expect(delegated.error === undefined).toBe(true)
            expect(isNativeObject(delegatedResult) && delegatedResult.isError !== true).toBe(true)
            const delegatedPayload = parseNativeObject(toolText(delegated))
            const task = delegatedPayload.record
            if (
                !isNativeObject(task) ||
                typeof task.taskId !== 'string' ||
                !task.taskId ||
                typeof task.attemptId !== 'string' ||
                !task.attemptId ||
                typeof task.leaseEpoch !== 'number' ||
                !task.leaseEpoch
            ) {
                throw new Error('native task delegation returned no fenced record')
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
                const statusResponse = await supervisorLines.next(
                    value => value.id === id,
                    15_000,
                    `native task status ${attempt + 1}`
                )
                const statusPayload = parseNativeObject(toolText(statusResponse))
                terminalState = typeof statusPayload.state === 'string' ? statusPayload.state : ''
                if (['completed', 'blocked', 'failed', 'cancelled', 'lost'].includes(terminalState))
                    break
                await new Promise(resolve => setTimeout(resolve, 1_000))
            }
            writeJsonLine(supervisor, {
                jsonrpc: '2.0',
                id: 200,
                method: 'tools/call',
                params: { name: 'tasks.result', arguments: { taskId: task.taskId } }
            })
            const taskResult = await supervisorLines.next(
                value => value.id === 200,
                15_000,
                'native read-only task result'
            )
            if (terminalState !== 'completed') {
                throw new Error(
                    `native read-only task did not complete; taskId=${task.taskId}; ${workerLines?.diagnostics() ?? 'worker unavailable'}`
                )
            }
            expect(terminalState).toBe('completed')
            expect(JSON.stringify(taskResult.result).includes(task.taskId)).toBe(true)
        } finally {
            if (worker?.stdin.writable)
                writeJsonLine(worker, {
                    schemaVersion: 1,
                    kind: 'shutdown',
                    requestId: 'native-e2e-shutdown'
                })
            await stopOwnedProcesses([supervisor, worker])
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it.skipIf(!native || process.platform !== 'darwin')(
        'routes packaged Electron ownership through the broker and preserves an independent Worker across restart',
        async () => {
            const cliBin = process.env['PAIR_CODEX_E2E_CLI_BIN']
            const appBin = process.env['PAIR_CODEX_E2E_APP_BIN']
            if (!cliBin || !appBin) {
                throw new Error('PAIR_CODEX_E2E_CLI_BIN and PAIR_CODEX_E2E_APP_BIN are required')
            }
            const codexBin = process.env['PAIR_CODEX_BIN'] || 'codex'
            const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-codex-electron-e2e-'))
            const userData = path.join(root, 'user-data')
            const independentRoot = path.join(root, 'independent')
            fs.mkdirSync(userData, { recursive: true, mode: 0o700 })
            fs.mkdirSync(independentRoot, { recursive: true, mode: 0o700 })

            const codexDir = path.join(userData, 'codex')
            fs.mkdirSync(codexDir, { recursive: true, mode: 0o700 })
            const owned = createNativeWorkerFixture(codexDir, codexBin, cliBin)
            const independent = createNativeWorkerFixture(independentRoot, codexBin, cliBin)
            runProtectedFile(
                executablePath(cliBin, 'nvpair-ui-broker'),
                'write',
                path.join(codexDir, 'config.json'),
                `${JSON.stringify(
                    {
                        schemaVersion: 1,
                        enabled: true,
                        workspaceRoot: owned.workspace,
                        stateRoot: owned.state,
                        codexExecutable: codexBin,
                        policyCeiling: 'read-only',
                        account: os.userInfo().username,
                        installationId: owned.installationId,
                        workerInstanceId: owned.workerInstanceId,
                        policyRevision: 1,
                        registrationName: 'pair-codex-supervisor'
                    },
                    null,
                    2
                )}\n`
            )

            const appEnv = {
                ...process.env,
                PAIR_RUN_CODEX_E2E: '1',
                PAIR_CODEX_E2E_USER_DATA: userData
            }
            let app: ChildProcessWithoutNullStreams | null = null
            let supervisor: ChildProcessWithoutNullStreams | null = null
            let independentWorker: ChildProcessWithoutNullStreams | null = null
            try {
                independentWorker = spawn(
                    executablePath(cliBin, 'nvpair-codex-worker'),
                    ['--managed-control'],
                    {
                        stdio: 'pipe'
                    }
                )
                const independentLines = new JsonLineProcess(independentWorker)
                writeJsonLine(independentWorker, {
                    schemaVersion: 1,
                    kind: 'start',
                    requestId: 'independent-start',
                    configRevision: 1,
                    configPath: independent.workerConfig,
                    workspaceAlias: 'independent'
                })
                await independentLines.next(
                    value => value.kind === 'ready',
                    15_000,
                    'independent Worker ready'
                )

                app = spawn(appBin, [], { env: appEnv, stdio: 'pipe' })
                const first = await waitForRuntimeDescriptor(path.join(codexDir, 'runtime.json'))
                const firstBootEpoch = first.bootEpoch
                if (typeof firstBootEpoch !== 'number')
                    throw new Error('runtime boot epoch is missing')

                supervisor = spawn(
                    executablePath(cliBin, 'nvpair-codex-supervisor'),
                    [
                        '--runtime-descriptor',
                        path.join(codexDir, 'runtime.json'),
                        '--state-root',
                        path.join(codexDir, 'supervisor-state'),
                        '--management-socket-dir',
                        path.join(codexDir, 'management')
                    ],
                    { stdio: 'pipe' }
                )
                const supervisorLines = new JsonLineProcess(supervisor)
                writeJsonLine(supervisor, {
                    jsonrpc: '2.0',
                    id: 300,
                    method: 'initialize',
                    params: {
                        protocolVersion: '2025-06-18',
                        capabilities: {},
                        clientInfo: { name: 'electron-e2e', version: '1' }
                    }
                })
                expect(
                    (await supervisorLines.next(value => value.id === 300)).error === undefined
                ).toBe(true)
                await waitForLocalWorker(supervisor, supervisorLines, 301)

                if (process.env['PAIR_RUN_MAIN_CODEX_E2E'] === '1') {
                    const supervisorCommand = executablePath(cliBin, 'nvpair-codex-supervisor')
                    const supervisorArgs = [
                        '--runtime-descriptor',
                        path.join(codexDir, 'runtime.json'),
                        '--state-root',
                        path.join(codexDir, 'supervisor-state'),
                        '--management-socket-dir',
                        path.join(codexDir, 'management')
                    ]
                    const mainProcess = spawnSync(
                        codexBin,
                        [
                            'exec',
                            '--ignore-user-config',
                            '--ignore-rules',
                            '--ephemeral',
                            '--approve-for-me',
                            '--cd',
                            owned.workspace,
                            '--model',
                            'gpt-5.6-sol',
                            '--config',
                            'model_reasoning_effort="max"',
                            '--config',
                            `mcp_servers.pair-codex-supervisor.command=${JSON.stringify(supervisorCommand)}`,
                            '--config',
                            `mcp_servers.pair-codex-supervisor.args=${JSON.stringify(supervisorArgs)}`,
                            'Call pair-codex-supervisor workers.list exactly once. If it returns worker id local, answer exactly PACKAGED_ELECTRON_MAIN_CODEX_OK. Do not call any other tool.'
                        ],
                        {
                            encoding: 'utf8',
                            timeout: 120_000,
                            maxBuffer: 4 << 20,
                            windowsHide: true
                        }
                    )
                    if (mainProcess.error || mainProcess.status !== 0)
                        throw new Error(
                            `Main Codex fixture failed; exit=${mainProcess.status ?? 'unavailable'}`
                        )
                    expect(mainProcess.stdout.includes('PACKAGED_ELECTRON_MAIN_CODEX_OK')).toBe(
                        true
                    )
                }

                await stopProcess(app)
                app = null
                expect(independentWorker.exitCode).toBeNull()

                app = spawn(appBin, [], { env: appEnv, stdio: 'pipe' })
                const restarted = await waitForRuntimeDescriptor(
                    path.join(codexDir, 'runtime.json'),
                    firstBootEpoch
                )
                expect(restarted.workerInstanceId).toBe(owned.workerInstanceId)
                await waitForLocalWorker(supervisor, supervisorLines, 400)
                expect(independentWorker.exitCode).toBeNull()
            } finally {
                if (independentWorker?.stdin.writable)
                    writeJsonLine(independentWorker, {
                        schemaVersion: 1,
                        kind: 'shutdown',
                        requestId: 'independent-shutdown'
                    })
                await stopOwnedProcesses([supervisor, app, independentWorker])
                fs.rmSync(root, { recursive: true, force: true })
            }
        },
        180_000
    )
})
