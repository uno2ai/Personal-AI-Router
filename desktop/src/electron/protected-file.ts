// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from 'node:child_process'
import path from 'node:path'

const MAX_FILE_BYTES = 4 << 20

// Keep native security in the broker. This module also serves isolated native
// fixtures, which provide the broker from their explicitly selected build.
export function runProtectedFile(
    brokerPath: string,
    operation: 'read' | 'read-owned-input' | 'write',
    filePath: string,
    content = ''
): string | null {
    if (!path.isAbsolute(brokerPath) || !path.isAbsolute(filePath)) {
        throw new Error('protected file paths must be absolute')
    }
    if (Buffer.byteLength(content, 'utf8') > MAX_FILE_BYTES) {
        throw new Error('protected file input exceeds 4 MiB')
    }
    const result = spawnSync(
        brokerPath,
        ['--protected-file', operation, '--protected-path', filePath],
        {
            input: content,
            encoding: 'utf8',
            windowsHide: true,
            timeout: 15_000,
            maxBuffer: MAX_FILE_BYTES + 1024
        }
    )
    if (result.error || result.signal) {
        throw new Error(
            'protected file helper unavailable; rebuild or reinstall the bundled nvpair-ui-broker'
        )
    }
    if (operation !== 'write' && result.status === 3) return null
    if (result.status !== 0) {
        throw new Error(
            `protected file ${operation} rejected; check file ownership, permissions, path links and the 4 MiB limit`
        )
    }
    return result.stdout
}
