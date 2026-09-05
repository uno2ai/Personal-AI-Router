import crypto from 'node:crypto'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'

import type {
    CodexConfig,
    CodexDesktopState,
    CodexMcpRegistration,
    CodexRegistrationSnapshot,
    CodexTaskMetadata,
    CodexTaskReference,
    CodexWorkerConfigInput,
    CodexWorkerStateSnapshot
} from '@/shared/types/codex'
import {
    CODEX_REGISTRATION_NAME,
    codexConfigPath,
    codexRuntimeDescriptorPath,
    codexWorkerConfigPath,
    loadCodexConfig,
    saveCodexConfig
} from './config-store'
import {
    applyMcpRegistration,
    fingerprintFile,
    getMcpRegistration,
    removeMcpRegistration
} from './mcp-registration'

interface ManagedWorkerConfig {
    schemaVersion: 1
    installationId: string
    workerInstanceId: string
    bootEpoch: number
    generation: number
    workspaceRoot: string
    stateRoot: string
    account: string
    codexBin: string
    maxConcurrency: number
    authToken: string
    runtimeDescriptorPath: string
    credentialRef: string
    credentialGeneration: number
    policyRevision: number
    artifactMaxBytes: number
    policyCeiling: CodexConfig['policyCeiling']
    workspaceAlias: string
    toolLabels: string[]
}

export class CodexManager {
    private readonly configFile: string

    constructor(
        private readonly userDataRoot: string,
        private readonly supervisorCommand: string,
        private readonly codexHome = resolveEffectiveCodexHome()
    ) {
        this.configFile = codexConfigPath(userDataRoot)
    }

    getState(): CodexDesktopState {
        const config = this.loadConfig()
        return {
            worker: this.workerState(config),
            registration: this.getMcpRegistration()
        }
    }

    configureWorker(input: CodexWorkerConfigInput): CodexDesktopState {
        if (!path.isAbsolute(input.workspaceRoot)) throw new Error('workspaceRoot must be absolute')
        const config = this.loadConfig()
        const stateRoot = config.stateRoot || path.join(this.userDataRoot, 'codex', 'worker-state')
        const next: CodexConfig = {
            ...config,
            workspaceRoot: input.workspaceRoot,
            stateRoot,
            codexExecutable: input.codexExecutable ?? (config.codexExecutable || process.env['CODEX_CLI_PATH'] || 'codex'),
            policyCeiling: input.policyCeiling ?? config.policyCeiling,
            account: input.account ?? (config.account || os.userInfo().username),
            registrationName: CODEX_REGISTRATION_NAME,
            policyRevision: config.policyRevision + 1
        }
        saveCodexConfig(this.configFile, next)
        this.writeManagedWorkerConfig(next)
        return this.getState()
    }

    setWorkerEnabled(enabled: boolean): CodexDesktopState {
        const config = this.loadConfig()
        if (enabled) {
            if (!config.workspaceRoot || !config.codexExecutable) {
                throw new Error('configure a workspace and Codex executable before enabling the Worker')
            }
            this.writeManagedWorkerConfig(config)
        }
        saveCodexConfig(this.configFile, { ...config, enabled, registrationName: CODEX_REGISTRATION_NAME })
        return this.getState()
    }

    getMcpRegistration(): CodexRegistrationSnapshot {
        const configPath = this.mainConfigPath()
        try {
            const current = getMcpRegistration(configPath)
            return {
                state: current ? 'registered' : 'unregistered',
                path: configPath,
                command: current?.command ?? null,
                args: current?.args ?? [],
                fingerprint: fingerprintFile(configPath)
            }
        } catch (error) {
            return {
                state: 'failed',
                path: configPath,
                command: null,
                args: [],
                fingerprint: fingerprintFile(configPath),
                error: error instanceof Error ? error.message : String(error)
            }
        }
    }

    applyMcpRegistration(): CodexRegistrationSnapshot {
        const configPath = this.mainConfigPath()
        const registration: CodexMcpRegistration = {
            command: this.supervisorCommand,
            args: [
                '--runtime-descriptor',
                codexRuntimeDescriptorPath(this.userDataRoot),
                '--state-root',
                path.join(this.userDataRoot, 'codex', 'supervisor-state'),
                '--management-socket-dir',
                path.join(this.userDataRoot, 'codex', 'management')
            ]
        }
        const current = fingerprintFile(configPath)
        const applied = applyMcpRegistration(configPath, registration, current)
        return {
            state: 'waiting_for_main',
            path: configPath,
            command: applied.command,
            args: applied.args,
            fingerprint: applied.fingerprint
        }
    }

    removeMcpRegistration(): CodexRegistrationSnapshot {
        const configPath = this.mainConfigPath()
        removeMcpRegistration(configPath, fingerprintFile(configPath))
        return this.getMcpRegistration()
    }

    async listTasks(): Promise<CodexTaskMetadata[]> {
        const response = await this.callManagement({ method: 'tasks.list' })
        if (!Array.isArray(response.tasks)) throw new Error('Supervisor returned an invalid task list')
        return response.tasks as CodexTaskMetadata[]
    }

    async cancelTask(reference: CodexTaskReference): Promise<void> {
        await this.callManagement({ method: 'tasks.cancel', ...reference })
    }

    private loadConfig(): CodexConfig {
        const config = loadCodexConfig(this.configFile)
        if (!fs.existsSync(this.configFile)) saveCodexConfig(this.configFile, config)
        return config
    }

    private workerState(config: CodexConfig): CodexWorkerStateSnapshot {
        if (!config.enabled) {
            return { state: 'disabled', enabled: false, policyCeiling: config.policyCeiling, endpoint: null, workerInstanceId: null, bootEpoch: null, policyRevision: config.policyRevision }
        }
        if (!config.workspaceRoot || !config.stateRoot || !config.codexExecutable) {
            return { state: 'setup_required', enabled: true, policyCeiling: config.policyCeiling, endpoint: null, workerInstanceId: config.workerInstanceId, bootEpoch: null, policyRevision: config.policyRevision }
        }
        const descriptorPath = codexRuntimeDescriptorPath(this.userDataRoot)
        if (!fs.existsSync(descriptorPath)) {
            return { state: 'starting', enabled: true, policyCeiling: config.policyCeiling, endpoint: null, workerInstanceId: config.workerInstanceId, bootEpoch: null, policyRevision: config.policyRevision }
        }
        try {
            const descriptor = JSON.parse(fs.readFileSync(descriptorPath, 'utf8')) as Record<string, unknown>
            const expiresAt = typeof descriptor.expiresAt === 'string' ? Date.parse(descriptor.expiresAt) : 0
            if (!expiresAt || expiresAt <= Date.now()) {
                return { state: 'failed', enabled: true, policyCeiling: config.policyCeiling, endpoint: null, workerInstanceId: config.workerInstanceId, bootEpoch: null, policyRevision: config.policyRevision, error: 'Worker runtime descriptor is expired' }
            }
            const descriptorError = typeof descriptor.error === 'string' ? descriptor.error : undefined
            let state: CodexWorkerStateSnapshot['state'] = 'starting'
            if (descriptor.state === 'busy') state = 'busy'
            else if (descriptor.state === 'ready') state = 'ready'
            else if (descriptor.state === 'stopped') state = 'stopped'
            else if (descriptor.state === 'unavailable') {
                const lower = descriptorError?.toLowerCase() ?? ''
                state = lower.includes('unsupported app-server')
                    ? 'incompatible'
                    : lower.includes('login') || lower.includes('account') || lower.includes('auth')
                      ? 'unauthorized'
                      : 'failed'
            }
            return {
                state,
                enabled: true,
                policyCeiling: config.policyCeiling,
                endpoint: typeof descriptor.endpoint === 'string' ? descriptor.endpoint : null,
                workerInstanceId: typeof descriptor.workerInstanceId === 'string' ? descriptor.workerInstanceId : config.workerInstanceId,
                bootEpoch: typeof descriptor.bootEpoch === 'number' ? descriptor.bootEpoch : null,
                policyRevision: config.policyRevision,
                ...(descriptorError ? { error: descriptorError } : {})
            }
        } catch (error) {
            return { state: 'failed', enabled: true, policyCeiling: config.policyCeiling, endpoint: null, workerInstanceId: config.workerInstanceId, bootEpoch: null, policyRevision: config.policyRevision, error: error instanceof Error ? error.message : String(error) }
        }
    }

    private writeManagedWorkerConfig(config: CodexConfig): void {
        const workerConfig: ManagedWorkerConfig = {
            schemaVersion: 1,
            installationId: config.installationId,
            workerInstanceId: config.workerInstanceId,
            bootEpoch: Date.now(),
            generation: 1,
            workspaceRoot: config.workspaceRoot,
            stateRoot: config.stateRoot,
            account: config.account,
            codexBin: config.codexExecutable,
            maxConcurrency: 1,
            authToken: crypto.randomBytes(32).toString('hex'),
            runtimeDescriptorPath: codexRuntimeDescriptorPath(this.userDataRoot),
            credentialRef: path.join(this.userDataRoot, 'codex', 'worker-credential.json'),
            credentialGeneration: config.policyRevision,
            policyRevision: config.policyRevision,
            artifactMaxBytes: 8 << 20,
            policyCeiling: config.policyCeiling,
            workspaceAlias: 'local',
            toolLabels: []
        }
        const filePath = codexWorkerConfigPath(this.userDataRoot)
        fs.mkdirSync(path.dirname(filePath), { recursive: true, mode: 0o700 })
        const temporary = `${filePath}.${crypto.randomUUID()}.tmp`
        const fd = fs.openSync(temporary, 'w', 0o600)
        try {
            fs.writeFileSync(fd, `${JSON.stringify(workerConfig, null, 2)}\n`, 'utf8')
            fs.fsyncSync(fd)
        } finally {
            fs.closeSync(fd)
        }
        fs.renameSync(temporary, filePath)
    }

    private mainConfigPath(): string {
        return path.join(this.codexHome, 'config.toml')
    }

    private async callManagement(request: Record<string, unknown>): Promise<Record<string, unknown>> {
        const directory = path.join(this.userDataRoot, 'codex', 'management')
        if (!fs.existsSync(directory)) throw new Error('Main Codex Supervisor is not running')
        const registries = fs
            .readdirSync(directory)
            .filter(name => name.startsWith('supervisor-') && name.endsWith('.json'))
            .sort()
        let lastError: Error | null = null
        for (const name of registries) {
            try {
                const registry = JSON.parse(fs.readFileSync(path.join(directory, name), 'utf8')) as {
                    schemaVersion?: unknown
                    socketPath?: unknown
                }
                if (registry.schemaVersion !== 1 || typeof registry.socketPath !== 'string') continue
                const response = await callManagementSocket(registry.socketPath, request)
                if (typeof response.error === 'string') throw new Error(response.error)
                return response
            } catch (error) {
                lastError = error instanceof Error ? error : new Error(String(error))
            }
        }
        throw lastError ?? new Error('Main Codex Supervisor is not running')
    }
}

function callManagementSocket(socketPath: string, request: Record<string, unknown>): Promise<Record<string, unknown>> {
    return new Promise((resolve, reject) => {
        const socket = net.createConnection(socketPath)
        let buffer = ''
        const timeout = setTimeout(() => {
            socket.destroy()
            reject(new Error('Supervisor management request timed out'))
        }, 1500)
        const finish = (callback: () => void): void => {
            clearTimeout(timeout)
            callback()
        }
        socket.on('error', error => finish(() => reject(error)))
        socket.on('connect', () => socket.write(`${JSON.stringify(request)}\n`))
        socket.on('data', chunk => {
            buffer += chunk.toString('utf8')
            const newline = buffer.indexOf('\n')
            if (newline < 0) return
            const line = buffer.slice(0, newline)
            try {
                const response: unknown = JSON.parse(line)
                if (!response || typeof response !== 'object' || Array.isArray(response)) {
                    throw new Error('Supervisor management response is not an object')
                }
                finish(() => resolve(response as Record<string, unknown>))
                socket.end()
            } catch (error) {
                finish(() => reject(error instanceof Error ? error : new Error(String(error))))
                socket.destroy()
            }
        })
    })
}

export function resolveEffectiveCodexHome(environment: NodeJS.ProcessEnv = process.env): string {
    const configured = environment['CODEX_HOME']?.trim()
    return configured && path.isAbsolute(configured) ? configured : path.join(os.homedir(), '.codex')
}

export function createCodexManager(userDataRoot: string, supervisorCommand: string): CodexManager {
    return new CodexManager(userDataRoot, supervisorCommand)
}
