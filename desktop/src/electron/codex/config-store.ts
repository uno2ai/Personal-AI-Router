import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'

import type { CodexConfig } from '@/shared/types/codex'

export const CODEX_CONFIG_SCHEMA_VERSION = 1 as const
export const CODEX_REGISTRATION_NAME = 'pair-codex-supervisor'

export function defaultCodexConfig(): CodexConfig {
    return {
        schemaVersion: CODEX_CONFIG_SCHEMA_VERSION,
        enabled: false,
        workspaceRoot: '',
        stateRoot: '',
        codexExecutable: '',
        policyCeiling: 'read-only',
        account: '',
        installationId: crypto.randomUUID(),
        workerInstanceId: crypto.randomUUID(),
        policyRevision: 1,
        registrationName: CODEX_REGISTRATION_NAME
    }
}

export function codexConfigPath(userDataRoot: string): string {
    return path.join(userDataRoot, 'codex', 'config.json')
}

export function codexWorkerConfigPath(userDataRoot: string): string {
    return path.join(userDataRoot, 'codex', 'worker-config.json')
}

export function codexRuntimeDescriptorPath(userDataRoot: string): string {
    return path.join(userDataRoot, 'codex', 'runtime.json')
}

export function loadCodexConfig(filePath: string): CodexConfig {
    if (!fs.existsSync(filePath)) return defaultCodexConfig()
    const raw = fs.readFileSync(filePath, 'utf8')
    const parsed: unknown = JSON.parse(raw)
    return validateCodexConfig(parsed)
}

export function saveCodexConfig(filePath: string, config: CodexConfig): void {
    const validated = validateCodexConfig(config)
    const directory = path.dirname(filePath)
    fs.mkdirSync(directory, { recursive: true, mode: 0o700 })
    if (fs.existsSync(filePath)) {
        const backup = `${filePath}.bak-${Date.now()}`
        fs.copyFileSync(filePath, backup)
        try {
            fs.chmodSync(backup, 0o600)
        } catch {
            // chmod is best effort on platforms without POSIX modes.
        }
        for (const name of fs.readdirSync(directory)) {
            if (!name.startsWith(`${path.basename(filePath)}.bak-`) || name === path.basename(backup)) continue
            try {
                fs.unlinkSync(path.join(directory, name))
            } catch {
                /* preserve the current config even if an old backup is locked */
            }
        }
    }
    const temporary = path.join(directory, `.${path.basename(filePath)}.${crypto.randomUUID()}.tmp`)
    const data = `${JSON.stringify(validated, null, 2)}\n`
    const fd = fs.openSync(temporary, 'w', 0o600)
    try {
        fs.writeFileSync(fd, data, 'utf8')
        fs.fsyncSync(fd)
    } finally {
        fs.closeSync(fd)
    }
    try {
        fs.chmodSync(temporary, 0o600)
    } catch {
        // chmod is best effort on platforms without POSIX modes.
    }
    fs.renameSync(temporary, filePath)
}

export function validateCodexConfig(value: unknown): CodexConfig {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
        throw new Error('Codex config must be an object')
    }
    const record = value as Record<string, unknown>
    const allowed = new Set([
        'schemaVersion',
        'enabled',
        'workspaceRoot',
        'stateRoot',
        'codexExecutable',
        'policyCeiling',
        'account',
        'installationId',
        'workerInstanceId',
        'policyRevision',
        'registrationName'
    ])
    for (const key of Object.keys(record)) {
        if (!allowed.has(key)) throw new Error(`Unknown Codex config field: ${key}`)
    }
    if (record.schemaVersion !== CODEX_CONFIG_SCHEMA_VERSION) throw new Error('Unsupported Codex config schema')
    if (typeof record.enabled !== 'boolean') throw new Error('Codex config enabled must be boolean')
    if (typeof record.workspaceRoot !== 'string' || typeof record.stateRoot !== 'string') {
        throw new Error('Codex config paths must be strings')
    }
    if (typeof record.codexExecutable !== 'string' || typeof record.account !== 'string') {
        throw new Error('Codex config executable and account must be strings')
    }
    if (record.policyCeiling !== 'read-only' && record.policyCeiling !== 'workspace-write') {
        throw new Error('Unsupported Codex policy ceiling')
    }
    if (typeof record.installationId !== 'string' || !record.installationId) {
        throw new Error('Codex installationId is required')
    }
    if (typeof record.workerInstanceId !== 'string' || !record.workerInstanceId) {
        throw new Error('Codex workerInstanceId is required')
    }
    if (typeof record.policyRevision !== 'number' || !Number.isInteger(record.policyRevision) || record.policyRevision <= 0) {
        throw new Error('Codex policyRevision must be a positive integer')
    }
    if (record.registrationName !== CODEX_REGISTRATION_NAME) {
        throw new Error('Unsupported Codex registration name')
    }
    return record as unknown as CodexConfig
}
