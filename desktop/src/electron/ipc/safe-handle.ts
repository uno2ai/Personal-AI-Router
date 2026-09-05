// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { BrowserWindow, ipcMain, IpcMainInvokeEvent } from 'electron'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import type {
    IpcResult,
    IpcChannelKey,
    IpcChannelMap,
    IpcHandlerFn
} from '@/shared/types/ipc-channels'
import getErrorString from '@/shared/utils/get-error-string'

export function isAllowedRendererNavigation(
    url: string,
    configuredRendererUrl: string,
    packagedRendererFile: string,
    isMainFrame: boolean
): boolean {
    if (!isMainFrame) return false
    if (configuredRendererUrl) {
        try {
            return new URL(url).origin === new URL(configuredRendererUrl).origin
        } catch {
            return false
        }
    }
    if (!packagedRendererFile) return false
    try {
        const actual = new URL(url)
        const expected = new URL(packagedRendererFile)
        return actual.protocol === 'file:' && expected.protocol === 'file:' && actual.pathname === expected.pathname
    } catch {
        return false
    }
}

function isKnownSender(event: IpcMainInvokeEvent): boolean {
    const senderWindow = BrowserWindow.fromWebContents(event.sender)
    if (!senderWindow) return false

    const url = event.sender.getURL()
    const packagedRendererFile = pathToFileURL(path.join(__dirname, '../ui/index.html')).toString()
    return isAllowedRendererNavigation(
        url,
        process.env['ELECTRON_RENDERER_URL'] ?? '',
        packagedRendererFile,
        event.senderFrame === event.sender.mainFrame
    )
}

type Req<K extends IpcChannelKey> = IpcChannelMap[K]['request']
type Res<K extends IpcChannelKey> = IpcChannelMap[K]['response']

/**
 * Type-safe IPC handler. Channel and handler signature are checked against IpcChannelMap.
 * The handler may return synchronously or asynchronously.
 *
 * This function is the single trust boundary between Electron's untyped `ipcMain.handle`
 * and our typed IPC system. The implementation cast bridges the generic fn to the
 * concrete call — callers see only the typed surface.
 */
export function safeHandle<K extends IpcChannelKey>(
    channel: K,
    fn: IpcHandlerFn<Req<K>, Res<K>, IpcMainInvokeEvent>
): void {
    const handler = fn as (event: IpcMainInvokeEvent, ...rest: unknown[]) => unknown
    ipcMain.handle(channel, async (event, ...args): Promise<IpcResult<Res<K>>> => {
        if (!isKnownSender(event)) {
            return { success: false, error: 'Unauthorized sender' }
        }
        try {
            const data = (await handler(event, ...args)) as Res<K>
            return { success: true, data }
        } catch (err) {
            const errorString = getErrorString(err) ?? String(err)
            return { success: false, error: errorString }
        }
    })
}
