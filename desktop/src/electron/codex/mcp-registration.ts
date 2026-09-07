// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'

import { CODEX_REGISTRATION_NAME } from './config-store'
import type { CodexMcpRegistration } from '@/shared/types/codex'

const OWNERSHIP_MARKER = '# Managed by PAIR Codex Desktop; ownership=v1'

export interface McpRegistrationSnapshot extends CodexMcpRegistration {
    fingerprint: string
}

interface BlockLocation {
    start: number
    end: number
    owned: boolean
}

export function fingerprintFile(filePath: string): string | null {
    if (!fs.existsSync(filePath)) return null
    return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex')
}

export function getMcpRegistration(filePath: string): McpRegistrationSnapshot | null {
    if (!fs.existsSync(filePath)) return null
    const text = fs.readFileSync(filePath, 'utf8')
    const location = locateBlock(text)
    if (!location || !location.owned) return null
    const lines = text.split(/\r?\n/).slice(location.start, location.end)
    const commandLine = lines.find(line => /^command\s*=/.test(line.trim()))
    const argsLine = lines.find(line => /^args\s*=/.test(line.trim()))
    if (!commandLine || !argsLine) throw new Error('Managed MCP entry is incomplete')
    const command = parseTomlString(commandLine)
    const args = parseTomlStringArray(argsLine)
    return { command, args, fingerprint: fingerprintFile(filePath) ?? '' }
}

export function applyMcpRegistration(
    filePath: string,
    registration: CodexMcpRegistration,
    expectedFingerprint?: string | null
): McpRegistrationSnapshot {
    const before = fingerprintFile(filePath)
    if (expectedFingerprint !== undefined && before !== expectedFingerprint) {
        throw new Error('Main Codex config changed concurrently; reload before applying')
    }
    const text = fs.existsSync(filePath) ? fs.readFileSync(filePath, 'utf8') : ''
    const location = locateBlock(text)
    if (location && !location.owned) {
        throw new Error(`MCP registration ${CODEX_REGISTRATION_NAME} is ambiguous or not PAIR-owned`)
    }
    const block = renderBlock(registration)
    const next = location
        ? replaceLines(text, location, block)
        : `${text.replace(/\s*$/, '')}${text.trim() ? '\n\n' : ''}${block}\n`
    atomicWriteWithBackup(filePath, next)
    const fingerprint = fingerprintFile(filePath)
    if (!fingerprint) throw new Error('MCP config disappeared after write')
    return { ...registration, fingerprint }
}

export function removeMcpRegistration(filePath: string, expectedFingerprint?: string | null): boolean {
    const before = fingerprintFile(filePath)
    if (expectedFingerprint !== undefined && before !== expectedFingerprint) {
        throw new Error('Main Codex config changed concurrently; reload before removing')
    }
    if (!fs.existsSync(filePath)) return false
    const text = fs.readFileSync(filePath, 'utf8')
    const location = locateBlock(text)
    if (!location) return false
    if (!location.owned) throw new Error(`MCP registration ${CODEX_REGISTRATION_NAME} is not PAIR-owned`)
    const next = replaceLines(text, location, '')
    atomicWriteWithBackup(filePath, next)
    return true
}

export function renderManagedMcpRegistration(registration: CodexMcpRegistration): string {
    return renderBlock(registration)
}

function renderBlock(registration: CodexMcpRegistration): string {
    if (!path.isAbsolute(registration.command)) throw new Error('Supervisor command must be absolute')
    if (registration.args.some(arg => typeof arg !== 'string')) throw new Error('Supervisor args must be strings')
    return `${OWNERSHIP_MARKER}\n[mcp_servers.${CODEX_REGISTRATION_NAME}]\ncommand = ${JSON.stringify(registration.command)}\nargs = ${JSON.stringify(registration.args)}\n`
}

function locateBlock(text: string): BlockLocation | null {
    const lines = text.split(/\r?\n/)
    const header = `[mcp_servers.${CODEX_REGISTRATION_NAME}]`
    const index = lines.findIndex(line => line.trim() === header)
    if (index < 0) return null
    let end = index + 1
    while (end < lines.length && !/^\s*\[/.test(lines[end])) end++
    const owned = index > 0 && lines[index - 1].trim() === OWNERSHIP_MARKER
    return { start: owned ? index - 1 : index, end, owned }
}

function replaceLines(text: string, location: BlockLocation, replacement: string): string {
    const lines = text.split(/\r?\n/)
    const replacementLines = replacement ? replacement.replace(/\n$/, '').split('\n') : []
    lines.splice(location.start, location.end - location.start, ...replacementLines)
    return `${lines.join('\n').replace(/\n{3,}/g, '\n\n').replace(/\s+$/, '')}\n`
}

function parseTomlString(line: string): string {
    const value = line.slice(line.indexOf('=') + 1).trim()
    const parsed: unknown = JSON.parse(value)
    if (typeof parsed !== 'string') throw new Error('MCP command is not a string')
    return parsed
}

function parseTomlStringArray(line: string): string[] {
    const value = line.slice(line.indexOf('=') + 1).trim()
    const parsed: unknown = JSON.parse(value)
    if (!Array.isArray(parsed) || parsed.some(item => typeof item !== 'string')) {
        throw new Error('MCP args are not a string array')
    }
    return parsed
}

function atomicWriteWithBackup(filePath: string, content: string): void {
    const directory = path.dirname(filePath)
    fs.mkdirSync(directory, { recursive: true, mode: 0o700 })
    if (fs.existsSync(filePath)) {
        const backup = `${filePath}.bak-${Date.now()}`
        fs.copyFileSync(filePath, backup)
        try {
            fs.chmodSync(backup, 0o600)
        } catch {
            /* best effort on Windows */
        }
    }
    const temporary = path.join(directory, `.${path.basename(filePath)}.${crypto.randomUUID()}.tmp`)
    const fd = fs.openSync(temporary, 'w', 0o600)
    try {
        fs.writeFileSync(fd, content, 'utf8')
        fs.fsyncSync(fd)
    } finally {
        fs.closeSync(fd)
    }
    try {
        fs.chmodSync(temporary, 0o600)
    } catch {
        /* best effort on Windows */
    }
    fs.renameSync(temporary, filePath)
}
