import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import {
    applyMcpRegistration,
    getMcpRegistration,
    removeMcpRegistration
} from '@/electron/codex/mcp-registration'
import {
    defaultCodexConfig,
    loadCodexConfig,
    saveCodexConfig
} from '@/electron/codex/config-store'
import { CodexManager, resolveEffectiveCodexHome } from '@/electron/codex/codex-manager'

describe('Codex configuration', () => {
    it('loads disabled-by-default config and preserves it atomically with a backup', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-codex-'))
        const configPath = path.join(root, 'codex', 'config.json')
        const initial = loadCodexConfig(configPath)
        expect(initial.enabled).toBe(false)
        expect(initial.policyCeiling).toBe('read-only')

        saveCodexConfig(configPath, { ...initial, enabled: true, workspaceRoot: path.join(root, 'workspace') })
        saveCodexConfig(configPath, { ...initial, enabled: false })

        expect(loadCodexConfig(configPath).enabled).toBe(false)
        expect(fs.readdirSync(path.dirname(configPath)).some(name => name.includes('.bak-'))).toBe(true)
        expect(fs.statSync(configPath).mode & 0o077).toBe(0)
    })

    it('preserves unrelated Main Codex MCP entries and removes only its managed entry', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-mcp-'))
        const configPath = path.join(root, 'config.toml')
        fs.writeFileSync(
            configPath,
            '[mcp_servers.other]\ncommand = "other"\n\n',
            { mode: 0o600 }
        )
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
            applyMcpRegistration(configPath, { command: '/pair/supervisor', args: [] }, 'not-current')
        ).toThrow(/changed|fingerprint/i)
    })
})

describe('Codex config defaults', () => {
    it('has a stable versioned default shape', () => {
        expect(defaultCodexConfig()).toMatchObject({
            schemaVersion: 1,
            enabled: false,
            policyCeiling: 'read-only'
        })
    })

    it('resolves CODEX_HOME explicitly instead of assuming the process cwd', () => {
        expect(resolveEffectiveCodexHome({ CODEX_HOME: '/tmp/effective-codex' } as NodeJS.ProcessEnv)).toBe(
            '/tmp/effective-codex'
        )
    })

    it('keeps secrets in the protected Worker config and reports disabled state', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-manager-'))
        const manager = new CodexManager(root, '/pair/supervisor', path.join(root, 'main-codex'))
        expect(manager.getState().worker.state).toBe('disabled')
        const configured = manager.configureWorker({ workspaceRoot: path.join(root, 'workspace') })
        expect(configured.worker.state).toBe('disabled')
        manager.setWorkerEnabled(true)
        const workerConfig = fs.readFileSync(path.join(root, 'codex', 'worker-config.json'), 'utf8')
        expect(workerConfig).toContain('authToken')
        expect(workerConfig).toContain('"policyCeiling": "read-only"')
        expect(fs.statSync(path.join(root, 'codex', 'worker-config.json')).mode & 0o077).toBe(0)
        expect(fs.readFileSync(path.join(root, 'codex', 'config.json'), 'utf8')).not.toContain('authToken')
    })
})
