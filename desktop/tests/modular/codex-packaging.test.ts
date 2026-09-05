import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

import {
    MODULAR_BUNDLED_BINARIES,
    MODULAR_RUNTIME_BINARIES,
    modularShippedBinaryBaseNames
} from '@/shared/constants/modular-binaries'

const root = path.resolve(__dirname, '../..')

describe('Codex packaging contract', () => {
    it('ships Worker and Supervisor exactly once with distinct lifecycle owners', () => {
        const shipped = modularShippedBinaryBaseNames()
        expect(shipped).toContain('nvpair-codex-worker')
        expect(shipped).toContain('nvpair-codex-supervisor')

        const worker = MODULAR_RUNTIME_BINARIES.find(
            item => item.baseName === 'nvpair-codex-worker'
        )
        expect(worker).toMatchObject({
            processName: 'codex-worker',
            launchOwner: 'broker',
            optional: true
        })
        expect(
            MODULAR_RUNTIME_BINARIES.some(item => item.baseName === 'nvpair-codex-supervisor')
        ).toBe(false)
        expect(
            MODULAR_BUNDLED_BINARIES.filter(item => item.baseName === 'nvpair-codex-supervisor')
        ).toHaveLength(1)
    })

    it('keeps packaged resource validation tied to the shipped binary whitelist', () => {
        const builder = fs.readFileSync(path.join(root, 'electron-builder.config.ts'), 'utf8')
        expect(builder).toContain("from: 'cli-bin'")
        expect(builder).toContain('modularShippedBinaryBaseNames')
        expect(builder).not.toContain('nvpair-codex-supervisor.exe')
    })

    it('keeps installer and runtime lifecycle scoped to PAIR-owned state', () => {
        const installer = fs.readFileSync(
            path.resolve(root, '../services/installer/nvpair-setup.nsi'),
            'utf8'
        )
        expect(installer).toContain('nvpair-codex-worker.exe')
        expect(installer).toContain('nvpair-codex-supervisor.exe')
        expect(installer).not.toContain('taskkill /F /IM "nvpair-codex-worker.exe"')
        expect(installer).not.toContain('taskkill /F /IM "nvpair-codex-supervisor.exe"')
        expect(installer).not.toContain('firewall add rule name="NVPAIR Codex Worker')
    })

    it('keeps the macOS uninstaller away from independently managed Codex processes', () => {
        const uninstaller = fs.readFileSync(
            path.join(root, 'scripts/build/macos/uninstall.sh'),
            'utf8'
        )
        expect(uninstaller).not.toContain('"nvpair-codex-worker"')
        expect(uninstaller).not.toContain('"nvpair-codex-supervisor"')
        expect(uninstaller).toContain('if [ "$PURGE_DATA" != "1" ]')
        expect(uninstaller).toContain('User data preserved')
        expect(uninstaller).toContain('rm -rf "$APP_PATH"')
    })
})
