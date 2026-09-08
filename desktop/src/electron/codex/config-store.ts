// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import net from 'node:net'

import { readProtectedConfig, writeProtectedConfig } from './protected-config'
import type { JsonValue } from '@/electron/service-bridge/json-rpc-subprocess'
import type { CodexConfig, CodexNetworkConfig } from '@/shared/types/codex'

const CODEX_CONFIG_SCHEMA_VERSION = 1
export const CODEX_REGISTRATION_NAME = 'pair-codex-supervisor'

export function defaultCodexConfig(): CodexConfig {
    return {
        network: { clusterDir: '', remoteListen: '', supervisorAllowlist: [], workerEndpoints: [] },
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
    const validated = validateCodexConfig({ ...config, network: { ...config.network } })
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

export function validateCodexConfig(value: JsonValue): CodexConfig {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
        throw new Error('Codex config must be an object')
    }
    const record = value
    const allowed = new Set([
        'schemaVersion',
        'network',
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
    if (
        record.policyCeiling !== 'read-only' &&
        record.policyCeiling !== 'workspace-write' &&
        record.policyCeiling !== 'danger-full-access'
    ) {
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
        network: validateCodexNetwork(record.network),
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

function validateCodexNetwork(value: JsonValue | undefined): CodexNetworkConfig {
    // Schema 1 predates network settings; old installations remain local-only.
    if (value === undefined) return defaultCodexConfig().network
    if (!value || typeof value !== 'object' || Array.isArray(value))
        throw new Error('Codex network settings must be an object')
    if (
        Object.keys(value).some(
            key =>
                !['clusterDir', 'remoteListen', 'supervisorAllowlist', 'workerEndpoints'].includes(
                    key
                )
        )
    )
        throw new Error('Unknown Codex network setting')
    if (
        typeof value.clusterDir !== 'string' ||
        typeof value.remoteListen !== 'string' ||
        !Array.isArray(value.supervisorAllowlist) ||
        !Array.isArray(value.workerEndpoints)
    )
        throw new Error('Invalid Codex network settings')
    const principal = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/
    const supervisorAllowlist: string[] = []
    for (const item of value.supervisorAllowlist) {
        if (typeof item !== 'string' || !principal.test(item))
            throw new Error('Invalid Supervisor principal')
        if (supervisorAllowlist.includes(item)) throw new Error('Duplicate Supervisor principal')
        supervisorAllowlist.push(item)
    }
    const workerEndpoints: string[] = []
    const ids = new Set<string>()
    for (const item of value.workerEndpoints) {
        if (typeof item !== 'string') throw new Error('Invalid Worker endpoint')
        const split = item.indexOf('=')
        const id = item.slice(0, split)
        if (split < 1 || !principal.test(id) || ids.has(id) || id === 'local')
            throw new Error('Worker endpoints require distinct peer principals')
        const url = new URL(item.slice(split + 1))
        const host = url.hostname.replace(/^\[|\]$/g, '')
        if (
            url.protocol !== 'https:' ||
            !net.isIP(host) ||
            Number(url.port || '443') < 1 ||
            url.username ||
            url.password ||
            url.search ||
            url.hash ||
            url.pathname !== '/' ||
            /[,\s]/.test(item)
        )
            throw new Error('Worker endpoint must be principal=https://IP:port')
        ids.add(id)
        workerEndpoints.push(`${id}=${url.origin}`)
    }
    const clusterDir = value.clusterDir.trim()
    const remoteListen = value.remoteListen.trim()
    if ((remoteListen || workerEndpoints.length) && !path.isAbsolute(clusterDir))
        throw new Error('An absolute PAIR cluster directory is required for remote connections')
    if (remoteListen) {
        const match = /^(\[[0-9a-fA-F:]+\]|[0-9.]+):(\d+)$/.exec(remoteListen)
        const host = match?.[1]?.replace(/^\[|\]$/g, '') ?? ''
        const port = Number(match?.[2])
        if (!net.isIP(host) || port < 1 || port > 65535 || !supervisorAllowlist.length)
            throw new Error('Remote listen requires IP:port and at least one allowed Supervisor')
    } else if (supervisorAllowlist.length) {
        throw new Error('Allowed Supervisors require a remote listen address')
    }
    return { clusterDir, remoteListen, supervisorAllowlist, workerEndpoints }
}
