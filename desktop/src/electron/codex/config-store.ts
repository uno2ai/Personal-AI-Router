// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'

import { readProtectedConfig, writeProtectedConfig } from './protected-config'
import type { JsonValue } from '@/electron/service-bridge/json-rpc-subprocess'
import type { CodexConfig } from '@/shared/types/codex'

const CODEX_CONFIG_SCHEMA_VERSION = 1
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
    const raw = readProtectedConfig(filePath)
    if (raw === null) return defaultCodexConfig()
    const parsed: JsonValue = JSON.parse(raw)
    return validateCodexConfig(parsed)
}

export function saveCodexConfig(filePath: string, config: CodexConfig): void {
    const validated = validateCodexConfig({ ...config })
    const directory = path.dirname(filePath)
    const previous = readProtectedConfig(filePath)
    if (previous !== null) {
        const backup = `${filePath}.bak-${crypto.randomUUID()}`
        writeProtectedConfig(backup, previous)
        for (const name of fs.readdirSync(directory)) {
            if (
                !name.startsWith(`${path.basename(filePath)}.bak-`) ||
                name === path.basename(backup)
            )
                continue
            try {
                fs.unlinkSync(path.join(directory, name))
            } catch {
                /* preserve the current config even if an old backup is locked */
            }
        }
    }
    writeProtectedConfig(filePath, `${JSON.stringify(validated, null, 2)}\n`)
}

function validateCodexConfig(value: JsonValue): CodexConfig {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
        throw new Error('Codex config must be an object')
    }
    const record = value
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
    if (record.schemaVersion !== CODEX_CONFIG_SCHEMA_VERSION)
        throw new Error('Unsupported Codex config schema')
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
    if (
        typeof record.policyRevision !== 'number' ||
        !Number.isInteger(record.policyRevision) ||
        record.policyRevision <= 0
    ) {
        throw new Error('Codex policyRevision must be a positive integer')
    }
    if (record.registrationName !== CODEX_REGISTRATION_NAME) {
        throw new Error('Unsupported Codex registration name')
    }
    return {
        schemaVersion: CODEX_CONFIG_SCHEMA_VERSION,
        enabled: record.enabled,
        workspaceRoot: record.workspaceRoot,
        stateRoot: record.stateRoot,
        codexExecutable: record.codexExecutable,
        account: record.account,
        policyCeiling: record.policyCeiling,
        installationId: record.installationId,
        workerInstanceId: record.workerInstanceId,
        policyRevision: record.policyRevision,
        registrationName: CODEX_REGISTRATION_NAME
    }
}
