// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useCodexStore } from '@/ui/stores/codex.store'
import type { CodexDesktopState, CodexTaskMetadata } from '@/shared/types/codex'

const disabledState: CodexDesktopState = {
    network: { clusterDir: '', remoteListen: '', supervisorAllowlist: [], workerEndpoints: [] },
    worker: {
        workspaceRoot: '',
        codexExecutable: '',
        state: 'disabled',
        enabled: false,
        policyCeiling: 'read-only',
        endpoint: null,
        workerInstanceId: null,
        bootEpoch: null,
        policyRevision: 1
    },
    registration: {
        state: 'unregistered',
        path: '/tmp/codex/config.toml',
        command: null,
        args: [],
        fingerprint: null
    }
}

const readyState: CodexDesktopState = {
    ...disabledState,
    worker: {
        ...disabledState.worker,
        state: 'ready',
        enabled: true,
        policyCeiling: 'read-only',
        endpoint: 'https://127.0.0.1:12345',
        workerInstanceId: 'worker-1',
        bootEpoch: 7
    }
}

const task: CodexTaskMetadata = {
    taskId: 'task-1',
    requestId: 'request-1',
    attemptId: 'attempt-1',
    leaseEpoch: 1,
    workerId: 'worker-1',
    state: 'acknowledged',
    createdAt: '2026-09-05T00:00:00Z',
    updatedAt: '2026-09-05T00:00:01Z'
}

function installApi(overrides: Partial<typeof window.windowApi.codex> = {}) {
    const api = {
        getState: vi.fn(async () => disabledState),
        configureWorker: vi.fn(async () => readyState),
        setWorkerEnabled: vi.fn(async () => readyState),
        getMcpRegistration: vi.fn(async () => disabledState.registration),
        applyMcpRegistration: vi.fn(async () => ({
            ...disabledState.registration,
            state: 'waiting_for_main' as const
        })),
        removeMcpRegistration: vi.fn(async () => disabledState.registration),
        listTasks: vi.fn(async () => [task]),
        cancelTask: vi.fn(async () => undefined),
        ...overrides
    }
    vi.stubGlobal('window', { windowApi: { codex: api } })
    return api
}

describe('Codex renderer store', () => {
    beforeEach(() => {
        useCodexStore.setState({ state: null, tasks: [], loading: false, error: null })
    })

    afterEach(() => {
        vi.unstubAllGlobals()
    })

    it('hydrates disabled-by-default and restrictive policy state', async () => {
        const api = installApi()
        await useCodexStore.getState().refresh()
        expect(api.getState).toHaveBeenCalledOnce()
        expect(useCodexStore.getState().state?.worker.state).toBe('disabled')
        expect(useCodexStore.getState().state?.worker.policyCeiling).toBe('read-only')
    })

    it('passes explicit write consent through the typed configuration call', async () => {
        const api = installApi()
        await useCodexStore.getState().configureWorker({
            workspaceRoot: '/tmp/workspace',
            policyCeiling: 'workspace-write'
        })
        expect(api.configureWorker).toHaveBeenCalledWith({
            workspaceRoot: '/tmp/workspace',
            policyCeiling: 'workspace-write'
        })
        expect(useCodexStore.getState().state?.worker.state).toBe('ready')
    })

    it('keeps task rows metadata-only and sends the opaque lease reference on cancel', async () => {
        const api = installApi()
        await useCodexStore.getState().refreshTasks()
        expect(useCodexStore.getState().tasks).toEqual([task])
        await useCodexStore.getState().cancelTask({
            taskId: task.taskId,
            attemptId: task.attemptId,
            leaseEpoch: task.leaseEpoch
        })
        expect(api.cancelTask).toHaveBeenCalledWith({
            taskId: 'task-1',
            attemptId: 'attempt-1',
            leaseEpoch: 1
        })
        expect(useCodexStore.getState().tasks).toEqual([task])
    })
})
