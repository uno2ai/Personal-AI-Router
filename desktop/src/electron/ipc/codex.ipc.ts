// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { app } from 'electron'
import path from 'node:path'

import { safeHandle } from '@/electron/ipc/safe-handle'
import { createCodexManager } from '@/electron/codex/codex-manager'
import { modularBinaryFileName } from '@/shared/constants/modular-binaries'
import { currentPlatform } from '@/shared/utils/platform'
import { restartConnector } from '@/electron/connector'

function supervisorCommand(): string {
    const root = app.isPackaged ? process.resourcesPath : app.getAppPath()
    return path.join(root, 'cli-bin', modularBinaryFileName('nvpair-codex-supervisor', currentPlatform()))
}

function manager() {
    return createCodexManager(app.getPath('userData'), supervisorCommand())
}

export function registerCodexIpc(): void {
    safeHandle('codex:get-state', () => manager().getState())
    safeHandle('codex:configure-worker', async (_event, input) => {
        const next = manager().configureWorker(input)
        await restartConnector()
        return manager().getState() ?? next
    })
    safeHandle('codex:set-worker-enabled', async (_event, { enabled }) => {
        const next = manager().setWorkerEnabled(enabled)
        await restartConnector()
        return manager().getState() ?? next
    })
    safeHandle('codex:get-mcp-registration', () => manager().getMcpRegistration())
    safeHandle('codex:apply-mcp-registration', () => manager().applyMcpRegistration())
    safeHandle('codex:remove-mcp-registration', () => manager().removeMcpRegistration())
    safeHandle('codex:list-tasks', () => manager().listTasks())
    safeHandle('codex:cancel-task', (_event, reference) => manager().cancelTask(reference))
}
