// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { IpcResult, MacHelperSetupStatus } from '@/shared/types/ipc-channels'
import { registerServiceIpc } from '@/electron/ipc/service.ipc'
import { serviceApi } from '@/preload/api/service.api'

const boundary = vi.hoisted(() => ({
    handlers: new Map<string, () => MacHelperSetupStatus | Promise<MacHelperSetupStatus>>(),
    status: vi.fn<() => MacHelperSetupStatus>(),
    setup: vi.fn<(manual: boolean) => Promise<void>>(),
    invoke: vi.fn<(channel: string) => Promise<IpcResult<MacHelperSetupStatus>>>()
}))

vi.mock('electron', () => ({ ipcRenderer: { invoke: boundary.invoke }, app: {}, shell: {} }))
vi.mock('@/electron/ipc/safe-handle', () => ({
    safeHandle: (
        channel: string,
        handler: () => MacHelperSetupStatus | Promise<MacHelperSetupStatus>
    ) => {
        boundary.handlers.set(channel, handler)
    }
}))
vi.mock('@/electron/services/mac-helper-setup', () => ({
    getMacHelperSetupStatus: boundary.status,
    runMacHelperSetup: boundary.setup
}))
vi.mock('@/electron/connector', () => ({}))
vi.mock('@/electron/config/ui-config', () => ({}))
vi.mock('@/electron/service-bridge/modular-supervisor', () => ({}))
vi.mock('@/shared/utils/log', () => ({}))

function handler(channel: string) {
    const result = boundary.handlers.get(channel)
    if (!result) throw new Error(`Missing IPC handler: ${channel}`)
    return result
}

describe('optional Mac helper IPC bridge', () => {
    beforeEach(() => {
        vi.clearAllMocks()
        boundary.handlers.clear()
        registerServiceIpc()
    })

    it('reads skipped status without initiating setup', () => {
        boundary.status.mockReturnValue({ supported: true, complete: false, skipped: true })
        expect(handler('service:get-mac-helper-status')()).toEqual({
            supported: true,
            complete: false,
            skipped: true
        })
        expect(boundary.setup).not.toHaveBeenCalled()
    })

    it('awaits explicit manual setup before returning fresh status', async () => {
        let finish = () => {}
        boundary.setup.mockImplementation(
            () =>
                new Promise<void>(resolve => {
                    finish = resolve
                })
        )
        const pending = handler('service:setup-mac-helper')()
        expect(boundary.setup).toHaveBeenCalledWith(true)
        expect(boundary.status).not.toHaveBeenCalled()
        boundary.status.mockReturnValue({ supported: true, complete: true, skipped: false })
        finish()
        await expect(pending).resolves.toEqual({ supported: true, complete: true, skipped: false })
    })

    it('leaves setup errors for safeHandle to return as failures', async () => {
        boundary.setup.mockRejectedValueOnce(new Error('A signed NVIDIA build is required'))
        await expect(handler('service:setup-mac-helper')()).rejects.toThrow('signed NVIDIA build')
        expect(boundary.status).not.toHaveBeenCalled()
    })

    it('unwraps status and setup through the named preload channels', async () => {
        const status = { supported: true, complete: false, skipped: true }
        boundary.invoke.mockResolvedValue({ success: true, data: status })
        await expect(serviceApi.getMacHelperStatus()).resolves.toEqual(status)
        expect(boundary.invoke).toHaveBeenLastCalledWith('service:get-mac-helper-status')
        await expect(serviceApi.setupMacHelper()).resolves.toEqual(status)
        expect(boundary.invoke).toHaveBeenLastCalledWith('service:setup-mac-helper')
    })

    it('rejects preload setup errors so the card can show an error and retry', async () => {
        boundary.invoke.mockResolvedValueOnce({ success: false, error: 'Setup cancelled' })
        await expect(serviceApi.setupMacHelper()).rejects.toThrow('Setup cancelled')
    })
})
