// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest'
import fs from 'fs'
import { initPlatform } from '@/electron/globals'
import { assertIsolated } from '../fixtures/isolation'
import {
    loadUiConfig,
    setMacHelperSetupComplete,
    setMacHelperSetupSkipped
} from '@/electron/config/ui-config'
import * as setup from '@/electron/services/mac-helper-setup'

const boundary = vi.hoisted(() => ({
    showMessageBox: vi.fn(),
    isSupported: vi.fn(() => true),
    ensureConfigured: vi.fn()
}))
vi.mock('electron', () => ({ dialog: { showMessageBox: boundary.showMessageBox } }))
vi.mock('@/electron/services/mac-privilege-service', () => ({ macPrivilege: boundary }))

describe('optional Mac helper setup', () => {
    beforeEach(() => {
        assertIsolated()
        const root = process.env.PAIR_USER_DATA ?? ''
        initPlatform({
            getUserData: () => root,
            getTemp: () => root,
            getResourcesPath: () => process.cwd(),
            getAppName: () => 'PAIR'
        })
        loadUiConfig()
        setMacHelperSetupComplete(false)
        setMacHelperSetupSkipped(false)
        boundary.isSupported.mockReturnValue(true)
        boundary.showMessageBox.mockResolvedValue({ response: 0 })
        boundary.ensureConfigured.mockResolvedValue({
            supported: true,
            registration: 'enabled',
            firewallConfigured: true,
            firewallError: null
        })
    })

    it('persists Skip without marking setup complete or invoking privileged operations', async () => {
        await setup.runMacHelperSetup()
        loadUiConfig()
        expect(setup.getMacHelperSetupStatus()).toEqual({
            supported: true,
            complete: false,
            skipped: true
        })
        expect(boundary.ensureConfigured).not.toHaveBeenCalled()
        boundary.showMessageBox.mockClear()
        await setup.runMacHelperSetup()
        expect(boundary.showMessageBox).not.toHaveBeenCalled()
        expect(boundary.ensureConfigured).not.toHaveBeenCalled()
    })

    it('allows explicit settings retry after Skip and records successful configuration', async () => {
        await setup.runMacHelperSetup(true)
        boundary.showMessageBox.mockResolvedValue({ response: 1 })
        await setup.runMacHelperSetup(true)
        loadUiConfig()
        expect(setup.getMacHelperSetupStatus()).toEqual({
            supported: true,
            complete: true,
            skipped: false
        })
        expect(boundary.ensureConfigured).toHaveBeenCalledExactlyOnceWith(true)
    })

    it('does not claim completion when approval is pending', async () => {
        boundary.showMessageBox.mockResolvedValue({ response: 1 })
        boundary.ensureConfigured.mockResolvedValue({
            supported: true,
            registration: 'requiresApproval',
            firewallConfigured: false,
            firewallError: null
        })
        await setup.runMacHelperSetup(true)
        expect(setup.getMacHelperSetupStatus().complete).toBe(false)
    })

    it('reports a manual setup error without preventing a later retry', async () => {
        boundary.showMessageBox.mockResolvedValue({ response: 1 })
        boundary.ensureConfigured.mockRejectedValueOnce(new Error('unsigned helper'))
        await expect(setup.runMacHelperSetup(true)).rejects.toThrow('unsigned helper')
        expect(setup.getMacHelperSetupStatus().complete).toBe(false)
        await setup.runMacHelperSetup(true)
        expect(setup.getMacHelperSetupStatus().complete).toBe(true)
    })

    it('does nothing on unsupported platforms', async () => {
        boundary.isSupported.mockReturnValue(false)
        await setup.runMacHelperSetup(true)
        expect(boundary.showMessageBox).not.toHaveBeenCalled()
        expect(boundary.ensureConfigured).not.toHaveBeenCalled()
    })

    it('keeps existing successful installations on the silent reconciliation path', async () => {
        setMacHelperSetupComplete(true)
        await setup.runMacHelperSetup()
        expect(boundary.showMessageBox).not.toHaveBeenCalled()
        expect(boundary.ensureConfigured).toHaveBeenCalledExactlyOnceWith(false)
    })

    it('does not mark a failed firewall configuration complete', async () => {
        boundary.showMessageBox.mockResolvedValue({ response: 1 })
        boundary.ensureConfigured.mockResolvedValue({
            supported: true,
            registration: 'enabled',
            firewallConfigured: false,
            firewallError: 'denied'
        })
        await expect(setup.runMacHelperSetup(true)).rejects.toThrow('denied')
        loadUiConfig()
        expect(setup.getMacHelperSetupStatus().complete).toBe(false)
    })

    it('clears stale completion when reconciliation needs approval', async () => {
        setMacHelperSetupComplete(true)
        boundary.ensureConfigured.mockResolvedValue({
            supported: true,
            registration: 'requiresApproval',
            firewallConfigured: false,
            firewallError: null
        })
        await setup.runMacHelperSetup()
        expect(setup.getMacHelperSetupStatus().complete).toBe(false)
    })

    it('clears stale completion on a native failure so settings can retry', async () => {
        setMacHelperSetupComplete(true)
        boundary.ensureConfigured.mockRejectedValueOnce(new Error('helper missing'))
        await expect(setup.runMacHelperSetup()).rejects.toThrow('helper missing')
        expect(setup.getMacHelperSetupStatus().complete).toBe(false)
    })

    it('reports failed Skip persistence but still avoids privileged operations this session', async () => {
        const rename = vi.spyOn(fs, 'renameSync').mockImplementationOnce(() => {
            throw new Error('disk full')
        })
        await expect(setup.runMacHelperSetup(true)).rejects.toThrow('disk full')
        expect(setup.getMacHelperSetupStatus().skipped).toBe(true)
        await setup.runMacHelperSetup()
        expect(boundary.ensureConfigured).not.toHaveBeenCalled()
        rename.mockRestore()
        loadUiConfig()
        expect(setup.getMacHelperSetupStatus().skipped).toBe(false)
    })

    it('coalesces simultaneous startup and settings requests into one consent dialog', async () => {
        let respond!: (value: { response: number }) => void
        boundary.showMessageBox.mockReturnValueOnce(
            new Promise(resolve => {
                respond = resolve
            })
        )
        const first = setup.runMacHelperSetup(true)
        const second = setup.runMacHelperSetup(true)
        respond({ response: 0 })
        await Promise.all([first, second])
        expect(boundary.showMessageBox).toHaveBeenCalledTimes(1)
        expect(boundary.ensureConfigured).not.toHaveBeenCalled()
    })
})
