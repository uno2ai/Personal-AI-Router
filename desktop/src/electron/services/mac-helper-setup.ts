// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { dialog } from 'electron'
import {
    isMacHelperSetupComplete,
    setMacHelperSetupComplete,
    isMacHelperSetupSkipped,
    setMacHelperSetupSkipped
} from '@/electron/config/ui-config'
import { APP_DISPLAY_NAME } from '@/shared/constants/app'
import { macPrivilege } from './mac-privilege-service'

export function getMacHelperSetupStatus(): {
    supported: boolean
    complete: boolean
    skipped: boolean
} {
    return {
        supported: macPrivilege.isSupported(),
        complete: isMacHelperSetupComplete(),
        skipped: isMacHelperSetupSkipped()
    }
}

let pending: Promise<void> | undefined

/** Startup and settings share one consent flow; closing the dialog is Skip. */
export function runMacHelperSetup(manual = false): Promise<void> {
    if (pending) return pending
    pending = configure(manual).finally(() => {
        pending = undefined
    })
    return pending
}

async function configure(manual: boolean): Promise<void> {
    if (!macPrivilege.isSupported()) return
    if (!manual && isMacHelperSetupSkipped()) return
    const firstTime = !isMacHelperSetupComplete()
    if (firstTime || manual) {
        const choice = await dialog.showMessageBox({
            type: 'info',
            buttons: ['Skip', 'Set up helper'],
            defaultId: 0,
            cancelId: 0,
            message: `${APP_DISPLAY_NAME} firewall helper (optional)`,
            detail: 'PAIR can continue without this helper. It configures macOS firewall rules for local AI services and requires administrator approval and an NVIDIA-signed build. If your devices already connect, including through Tailscale, you can skip it. You can retry later in Settings.'
        })
        if (choice.response !== 1) {
            setMacHelperSetupSkipped(true)
            return
        }
        setMacHelperSetupSkipped(false)
    }
    // Completion is a successful reconciliation, not a permanent historical flag.
    setMacHelperSetupComplete(false)
    const result = await macPrivilege.ensureConfigured(firstTime || manual)
    if (!result.supported) return
    if (result.registration === 'requiresApproval') {
        if (firstTime || manual)
            await dialog.showMessageBox({
                type: 'info',
                buttons: ['OK'],
                message: 'Optional helper is awaiting macOS approval',
                detail: `To finish, open System Settings > General > Login Items & Extensions and enable the ${APP_DISPLAY_NAME} background item. PAIR can continue without it.`
            })
        return
    }
    if (result.registration !== 'enabled' || !result.firewallConfigured) {
        throw new Error(
            `Optional firewall helper setup incomplete: ${result.registration} ${result.firewallError ?? ''}`
        )
    }
    setMacHelperSetupComplete(true)
}
