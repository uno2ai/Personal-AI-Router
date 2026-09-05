// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest'
import {
    MODULAR_BUNDLED_BINARIES,
    MODULAR_RUNTIME_BINARIES,
    modularShippedBinaryBaseNames
} from '@/shared/constants/modular-binaries'

describe('Codex modular binary inventory', () => {
    it('ships the Worker runtime and Supervisor command', () => {
        expect(modularShippedBinaryBaseNames()).toEqual(
            expect.arrayContaining(['nvpair-codex-worker', 'nvpair-codex-supervisor'])
        )

        const worker = MODULAR_RUNTIME_BINARIES.find(
            definition => definition.processName === 'codex-worker'
        )
        expect(worker).toMatchObject({
            baseName: 'nvpair-codex-worker',
            launchOwner: 'broker',
            optional: true,
            needsFirewallAccess: false
        })

        expect(MODULAR_BUNDLED_BINARIES).toContainEqual({
            baseName: 'nvpair-codex-supervisor'
        })
        expect(
            MODULAR_RUNTIME_BINARIES.some(
                definition => definition.baseName === 'nvpair-codex-supervisor'
            )
        ).toBe(false)
    })
})
