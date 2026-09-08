// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import {
    applyMcpRegistration,
    getMcpRegistration,
    removeMcpRegistration
} from '@/electron/codex/mcp-registration'
import { defaultCodexConfig, loadCodexConfig, saveCodexConfig } from '@/electron/codex/config-store'
import { writeProtectedConfig } from '@/electron/codex/protected-config'
import * as protectedConfig from '@/electron/codex/protected-config'
import { runProtectedFile } from '@/electron/protected-file'
import { CodexManager, resolveEffectiveCodexHome } from '@/electron/codex/codex-manager'

const brokerDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-test-broker-'))
vi.mock('@/electron/cli-bin', () => ({ getCliBinDir: () => brokerDirectory }))
beforeAll(() => {
    execFileSync(
        'go',
        [
            'build',
            '-o',
            path.join(
                brokerDirectory,
                `nvpair-ui-broker${process.platform === 'win32' ? '.exe' : ''}`
            ),
            '.'
        ],
        {
            cwd: path.resolve('..', 'services', 'nvpair-ui-broker'),
            timeout: 120_000
        }
    )
}, 120_000)
afterAll(() => fs.rmSync(brokerDirectory, { recursive: true, force: true }))

function assertPrivate(filePath: string): void {
    if (process.platform !== 'win32') {
        expect(fs.statSync(filePath).mode & 0o077).toBe(0)
        return
    }
    const result = execFileSync(
        'powershell.exe',
        [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            '$acl = [System.IO.File]::GetAccessControl($env:PAIR_TEST_ACL_PATH); $sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value; $allowed = @($sid, "S-1-5-18", "S-1-5-32-544"); if (-not $acl.AreAccessRulesProtected) { exit 2 }; foreach ($rule in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) { if ($rule.AccessControlType -eq "Allow" -and $allowed -notcontains $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value) { exit 3 } }; "private"'
        ],
        {
            encoding: 'utf8',
            env: {
                ...process.env,
                PAIR_TEST_ACL_PATH: filePath,
                PSModulePath: `${process.env.SystemRoot}\\System32\\WindowsPowerShell\\v1.0\\Modules`
            }
        }
    )
    expect(result.trim()).toBe('private')
}

describe('Codex configuration', () => {
    it('fails closed with an actionable missing-broker error and rejects oversized input', () => {
        const destination = path.join(brokerDirectory, 'not-written.json')
        const unavailable = path.join(brokerDirectory, 'missing-broker')
        expect(() =>
            runProtectedFile(unavailable, 'write', destination, 'private-fixture')
        ).toThrow(/rebuild or reinstall/)
        expect(() =>
            runProtectedFile(unavailable, 'write', destination, 'x'.repeat((4 << 20) + 1))
        ).toThrow(/4 MiB/)
        expect(fs.existsSync(destination)).toBe(false)
    })
    it('rejects junction traversal without writing outside its directory', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-junction-'))
        const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-outside-'))
        try {
            fs.symlinkSync(
                outside,
                path.join(root, 'escape'),
                process.platform === 'win32' ? 'junction' : 'dir'
            )
            expect(() =>
                saveCodexConfig(path.join(root, 'escape', 'config.json'), defaultCodexConfig())
            ).toThrow()
            expect(fs.readdirSync(outside)).toEqual([])
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
            fs.rmSync(outside, { recursive: true, force: true })
        }
    })

    it('preserves inherited Main input on preview and privately writes content and backup', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), "pair spaces & ' $-"))
        const configPath = path.join(root, 'config.toml')
        const original = '[mcp_servers.other]\ncommand = "other"\n'
        try {
            fs.writeFileSync(configPath, original)
            const before = fs.statSync(configPath)
            expect(getMcpRegistration(configPath)).toBeNull()
            expect(fs.statSync(configPath).mode).toBe(before.mode)
            expect(fs.readFileSync(configPath, 'utf8')).toBe(original)
            if (process.platform === 'win32') expect(() => assertPrivate(configPath)).toThrow()
            applyMcpRegistration(configPath, { command: path.join(root, 'supervisor'), args: [] })
            assertPrivate(configPath)
            const backups = fs.readdirSync(root).filter(name => name.includes('.bak-'))
            expect(backups).toHaveLength(1)
            assertPrivate(path.join(root, backups[0]))
            expect(fs.readFileSync(path.join(root, backups[0]), 'utf8')).toBe(original)
            expect(fs.readFileSync(configPath, 'utf8')).toContain(original.trim())
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it('rejects managed config with inherited ACLs or broad modes without repairing it', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-managed-input-'))
        const configPath = path.join(root, 'config.json')
        try {
            fs.writeFileSync(configPath, JSON.stringify(defaultCodexConfig()), { mode: 0o644 })
            expect(() => loadCodexConfig(configPath)).toThrow(/protected/)
            expect(() => saveCodexConfig(configPath, defaultCodexConfig())).toThrow(/protected/)
            expect(fs.readdirSync(root)).toEqual(['config.json'])
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it('loads disabled-by-default config and preserves it atomically with a backup', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-codex-'))
        const configPath = path.join(root, 'codex', 'config.json')
        const initial = loadCodexConfig(configPath)
        expect(initial.enabled).toBe(false)
        expect(initial.policyCeiling).toBe('read-only')

        saveCodexConfig(configPath, {
            ...initial,
            enabled: true,
            workspaceRoot: path.join(root, 'workspace')
        })
        saveCodexConfig(configPath, { ...initial, enabled: false })

        expect(loadCodexConfig(configPath).enabled).toBe(false)
        expect(fs.readdirSync(path.dirname(configPath)).some(name => name.includes('.bak-'))).toBe(
            true
        )
        assertPrivate(configPath)
        for (const name of fs.readdirSync(path.dirname(configPath)))
            assertPrivate(path.join(path.dirname(configPath), name))
    })

    it('preserves unrelated Main Codex MCP entries and removes only its managed entry', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-mcp-'))
        const configPath = path.join(root, 'config.toml')
        fs.writeFileSync(configPath, '[mcp_servers.other]\ncommand = "other"\n\n', { mode: 0o600 })
        const registration = {
            command: '/Applications/PAIR.app/Contents/Resources/cli-bin/nvpair-codex-supervisor',
            args: ['--runtime-descriptor', path.join(root, 'runtime.json')]
        }
        applyMcpRegistration(configPath, registration)
        const applied = fs.readFileSync(configPath, 'utf8')
        expect(applied).toContain('[mcp_servers.other]')
        expect(applied).toContain('[mcp_servers.pair-codex-supervisor]')
        expect(getMcpRegistration(configPath)?.command).toBe(registration.command)

        removeMcpRegistration(configPath)
        const removed = fs.readFileSync(configPath, 'utf8')
        expect(removed).toContain('[mcp_servers.other]')
        expect(removed).not.toContain('[mcp_servers.pair-codex-supervisor]')
    })

    it('refuses to replace an unowned same-name MCP entry', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-mcp-ambiguous-'))
        const configPath = path.join(root, 'config.toml')
        fs.writeFileSync(
            configPath,
            '[mcp_servers.pair-codex-supervisor]\ncommand = "user-owned"\n\n',
            { mode: 0o600 }
        )
        expect(() =>
            applyMcpRegistration(configPath, { command: '/pair/supervisor', args: [] })
        ).toThrow(/ambiguous|owned/i)
    })

    it('rejects concurrent edits using the original fingerprint', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-mcp-race-'))
        const configPath = path.join(root, 'config.toml')
        fs.writeFileSync(configPath, '# original\n', { mode: 0o600 })
        expect(() =>
            applyMcpRegistration(
                configPath,
                { command: '/pair/supervisor', args: [] },
                'not-current'
            )
        ).toThrow(/changed|fingerprint/i)
    })
})

describe('Codex config defaults', () => {
    it('keeps both saved files unchanged when remote settings cannot be written', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-rollback-'))
        try {
            const manager = new CodexManager(root, '/pair/supervisor', path.join(root, 'main'))
            const network = {
                clusterDir: root,
                remoteListen: '100.64.0.1:14324',
                supervisorAllowlist: ['peer'],
                workerEndpoints: []
            }
            manager.configureWorker({ workspaceRoot: root, network })
            const workerFile = path.join(root, 'codex', 'worker-config.json')
            const previous = fs.readFileSync(workerFile, 'utf8')
            const spy = vi
                .spyOn(protectedConfig, 'writeProtectedConfig')
                .mockImplementationOnce(() => {
                    throw new Error('injected write failure')
                })
            try {
                expect(() =>
                    manager.configureWorker({
                        workspaceRoot: root,
                        network: defaultCodexConfig().network
                    })
                ).toThrow('injected write failure')
            } finally {
                spy.mockRestore()
            }
            expect(manager.getState().network).toEqual(network)
            expect(fs.readFileSync(workerFile, 'utf8')).toBe(previous)
            const actualWrite = protectedConfig.writeProtectedConfig
            let injected = false
            const lateFailure = vi
                .spyOn(protectedConfig, 'writeProtectedConfig')
                .mockImplementation((file, content) => {
                    if (file === path.join(root, 'codex', 'config.json') && !injected) {
                        injected = true
                        throw new Error('injected late write failure')
                    }
                    actualWrite(file, content)
                })
            try {
                expect(() =>
                    manager.configureWorker({
                        workspaceRoot: root,
                        network: defaultCodexConfig().network
                    })
                ).toThrow('injected late write failure')
            } finally {
                lateFailure.mockRestore()
            }
            expect(injected).toBe(true)
            expect(manager.getState().network).toEqual(network)
            expect(fs.readFileSync(workerFile, 'utf8')).toBe(previous)
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it('normalizes HTTPS port 443 and rejects port zero', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-ports-'))
        try {
            const file = path.join(root, 'config.json')
            const config = defaultCodexConfig()
            config.network = {
                ...config.network,
                clusterDir: root,
                workerEndpoints: ['peer=https://100.64.0.2:443']
            }
            saveCodexConfig(file, config)
            expect(loadCodexConfig(file).network.workerEndpoints).toEqual([
                'peer=https://100.64.0.2'
            ])
            config.network.workerEndpoints = ['peer=https://100.64.0.2:0']
            expect(() => saveCodexConfig(file, config)).toThrow()
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })
    it('persists explicit YOLO policy into the managed Worker and restores it without changing defaults', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-yolo-config-'))
        try {
            expect(defaultCodexConfig().policyCeiling).toBe('read-only')
            const manager = new CodexManager(root, '/pair/supervisor', path.join(root, 'main'))
            manager.configureWorker({ workspaceRoot: root, policyCeiling: 'danger-full-access' })
            const managed = JSON.parse(
                fs.readFileSync(path.join(root, 'codex', 'worker-config.json'), 'utf8')
            )
            expect(managed.policyCeiling).toBe('danger-full-access')
            const reopened = new CodexManager(root, '/pair/supervisor', path.join(root, 'main'))
            expect(reopened.getState().worker.policyCeiling).toBe('danger-full-access')
            expect(reopened.applyMcpRegistration().args).toEqual(
                expect.arrayContaining(['--default-task-mode', 'yolo'])
            )
            expect(getMcpRegistration(path.join(root, 'main', 'config.toml'))).toMatchObject({
                defaultToolsApprovalMode: 'approve'
            })
            const mainConfig = path.join(root, 'main', 'config.toml')
            writeProtectedConfig(
                mainConfig,
                fs
                    .readFileSync(mainConfig, 'utf8')
                    .replace(
                        'default_tools_approval_mode = "approve"',
                        'default_tools_approval_mode = "prompt"'
                    )
            )
            expect(reopened.getMcpRegistration().state).toBe('failed')
            expect(reopened.applyMcpRegistration().state).toBe('waiting_for_main')
            reopened.configureWorker({ workspaceRoot: root, policyCeiling: 'read-only' })
            expect(reopened.getMcpRegistration().state).toBe('failed')
            expect(reopened.applyMcpRegistration().args).not.toContain('--default-task-mode')
            expect(getMcpRegistration(path.join(root, 'main', 'config.toml'))).not.toHaveProperty(
                'defaultToolsApprovalMode',
                'approve'
            )
            expect(
                JSON.parse(fs.readFileSync(path.join(root, 'codex', 'worker-config.json'), 'utf8'))
                    .policyCeiling
            ).toBe('read-only')
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })
    it('restores remote settings and wires both managed Worker and MCP registration', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-remote-config-'))
        try {
            const manager = new CodexManager(root, '/pair/supervisor', path.join(root, 'main'))
            const network = {
                clusterDir: path.join(root, 'cluster'),
                remoteListen: '100.64.0.1:14324',
                supervisorAllowlist: ['peer-main'],
                workerEndpoints: ['peer-worker=https://100.64.0.2:14324']
            }
            manager.configureWorker({ workspaceRoot: root, network })
            const reopened = new CodexManager(root, '/pair/supervisor', path.join(root, 'main'))
            expect(reopened.getState().network).toEqual(network)
            const managed = JSON.parse(
                fs.readFileSync(path.join(root, 'codex', 'worker-config.json'), 'utf8')
            )
            expect(managed).toMatchObject({
                remoteListen: network.remoteListen,
                clusterDir: network.clusterDir,
                supervisorAllowlist: network.supervisorAllowlist,
                policyCeiling: 'read-only'
            })
            expect(reopened.applyMcpRegistration().args).toEqual(
                expect.arrayContaining([
                    '--runtime-descriptor',
                    '--cluster-dir',
                    network.clusterDir,
                    '--worker-endpoints',
                    network.workerEndpoints[0]
                ])
            )
            reopened.configureWorker({
                workspaceRoot: root,
                network: { ...network, workerEndpoints: [] }
            })
            expect(reopened.getMcpRegistration()).toMatchObject({
                state: 'failed',
                error: expect.stringContaining('Apply registration')
            })
            expect(reopened.applyMcpRegistration().args).not.toContain('--worker-endpoints')
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it('migrates previous schema-1 files to disabled remote access', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-old-network-'))
        try {
            const { network: _network, ...oldConfig } = defaultCodexConfig()
            const file = path.join(root, 'config.json')
            writeProtectedConfig(file, JSON.stringify(oldConfig))
            expect(loadCodexConfig(file).network).toEqual(defaultCodexConfig().network)
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it('rejects unsafe remote settings before replacing saved configuration', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-network-invalid-'))
        const file = path.join(root, 'config.json')
        const base = defaultCodexConfig()
        try {
            saveCodexConfig(file, base)
            for (const workerEndpoints of [
                ['peer=http://100.64.0.2:14324'],
                ['peer=https://user:secret@100.64.0.2:14324'],
                ['peer=https://100.64.0.2:14324/path'],
                ['local=https://100.64.0.2:14324'],
                ['peer=https://100.64.0.2:14324', 'peer=https://100.64.0.3:14324']
            ]) {
                expect(() =>
                    saveCodexConfig(file, {
                        ...base,
                        network: {
                            ...base.network,
                            clusterDir: root,
                            workerEndpoints
                        }
                    })
                ).toThrow()
            }
            expect(() =>
                saveCodexConfig(file, {
                    ...base,
                    network: {
                        ...base.network,
                        clusterDir: root,
                        remoteListen: '100.64.0.1:14324'
                    }
                })
            ).toThrow(/allowed Supervisor/)
            expect(loadCodexConfig(file)).toEqual(base)
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })
    it('has a stable versioned default shape', () => {
        expect(defaultCodexConfig()).toMatchObject({
            schemaVersion: 1,
            enabled: false,
            policyCeiling: 'read-only'
        })
    })

    it('resolves CODEX_HOME explicitly instead of assuming the process cwd', () => {
        expect(resolveEffectiveCodexHome({ CODEX_HOME: '/tmp/effective-codex' })).toBe(
            '/tmp/effective-codex'
        )
    })

    it('keeps secrets in the protected Worker config and reports disabled state', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-manager-'))
        const manager = new CodexManager(root, '/pair/supervisor', path.join(root, 'main-codex'))
        expect(manager.getState().worker.state).toBe('disabled')
        const configured = manager.configureWorker({ workspaceRoot: path.join(root, 'workspace') })
        const reopened = new CodexManager(root, '/pair/supervisor', path.join(root, 'main-codex'))
        expect(reopened.getState().worker).toMatchObject({
            workspaceRoot: path.join(root, 'workspace'),
            codexExecutable: configured.worker.codexExecutable
        })
        expect(configured.worker.codexExecutable).not.toBe('')
        expect(configured.worker.state).toBe('disabled')
        expect(configured.worker.policyCeiling).toBe('read-only')
        manager.setWorkerEnabled(true)
        const workerConfig = fs.readFileSync(path.join(root, 'codex', 'worker-config.json'), 'utf8')
        expect(workerConfig).toContain('authToken')
        expect(workerConfig).toContain('"account": "')
        expect(workerConfig).toContain('"policyCeiling": "read-only"')
        assertPrivate(path.join(root, 'codex', 'worker-config.json'))
        expect(fs.readFileSync(path.join(root, 'codex', 'config.json'), 'utf8')).not.toContain(
            'authToken'
        )
    })

    it('reports Main Codex activation separately from a written registration', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-manager-registration-'))
        const mainCodex = path.join(root, 'main-codex')
        const manager = new CodexManager(root, '/pair/supervisor', mainCodex)
        const applied = manager.applyMcpRegistration()
        expect(applied.state).toBe('waiting_for_main')

        const management = path.join(root, 'codex', 'management')
        fs.mkdirSync(management, { recursive: true, mode: 0o700 })
        writeProtectedConfig(
            path.join(management, `supervisor-${process.pid}.json`),
            JSON.stringify({
                schemaVersion: 1,
                pid: process.pid,
                configurationId: applied.args.at(-1),
                socketPath: path.join(management, 'supervisor.sock')
            })
        )
        expect(manager.getMcpRegistration().state).toBe('connected')
        manager.configureWorker({
            workspaceRoot: root,
            network: {
                clusterDir: root,
                remoteListen: '',
                supervisorAllowlist: [],
                workerEndpoints: ['peer=https://100.64.0.2:14324']
            }
        })
        manager.applyMcpRegistration()
        expect(manager.getMcpRegistration().state).toBe('waiting_for_main')
    })
})
