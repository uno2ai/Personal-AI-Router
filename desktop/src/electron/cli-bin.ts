// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import path from 'node:path'
import { app } from 'electron'

export function getCliBinDir(): string {
    return path.join(app.isPackaged ? process.resourcesPath : app.getAppPath(), 'cli-bin')
}
