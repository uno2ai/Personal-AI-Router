// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { resolveCodexE2EUserData } from '@/electron/e2e-user-data'

describe('packaged Codex E2E userData override', () => {
    it('accepts an existing directory only when the native E2E gate is explicit', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-e2e-user-data-'))
        try {
            expect(resolveCodexE2EUserData(false, root, os.tmpdir())).toBeNull()
            expect(resolveCodexE2EUserData(true, root, os.tmpdir())).toBe(fs.realpathSync(root))
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
        }
    })

    it('rejects paths outside the temporary root and symlink escapes', () => {
        const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pair-e2e-user-data-'))
        const outside = fs.mkdtempSync(path.join(path.dirname(os.tmpdir()), 'pair-e2e-outside-'))
        const link = path.join(root, 'escape')
        try {
            fs.symlinkSync(outside, link, process.platform === 'win32' ? 'junction' : 'dir')
            expect(() => resolveCodexE2EUserData(true, outside, os.tmpdir())).toThrow(
                'must resolve beneath the OS temporary directory'
            )
            expect(() => resolveCodexE2EUserData(true, link, os.tmpdir())).toThrow(
                'must resolve beneath the OS temporary directory'
            )
        } finally {
            fs.rmSync(root, { recursive: true, force: true })
            fs.rmSync(outside, { recursive: true, force: true })
        }
    })
})
