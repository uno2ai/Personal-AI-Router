// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import path from 'node:path'
import { getCliBinDir } from '@/electron/cli-bin'
import { runProtectedFile } from '@/electron/protected-file'
import { modularBinaryFileName } from '@/shared/constants/modular-binaries'
import { currentPlatform } from '@/shared/utils/platform'

function brokerPath(): string {
    return path.join(getCliBinDir(), modularBinaryFileName('nvpair-ui-broker', currentPlatform()))
}

export function readProtectedConfig(filePath: string): string | null {
    return runProtectedFile(brokerPath(), 'read', filePath)
}

// Only external Main Codex input may carry inherited or broad permissions.
export function readMainCodexInput(filePath: string): string | null {
    return runProtectedFile(brokerPath(), 'read-owned-input', filePath)
}

export function writeProtectedConfig(filePath: string, content: string): void {
    runProtectedFile(brokerPath(), 'write', filePath, content)
}
