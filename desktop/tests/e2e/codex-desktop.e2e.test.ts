import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

const native = process.env['PAIR_RUN_CODEX_E2E'] === '1'

describe('packaged Codex Desktop native contract', () => {
    it.skipIf(!native)('contains both packaged Codex binaries and a manifest', () => {
        const cliBin = process.env['PAIR_CODEX_E2E_CLI_BIN']
        if (!cliBin) throw new Error('PAIR_CODEX_E2E_CLI_BIN is required for native Codex E2E')
        const manifestPath = path.join(cliBin, 'manifest.json')
        const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8')) as {
            files?: Array<{ fileName?: string }>
        }
        const names = new Set((manifest.files ?? []).map(file => file.fileName))
        const suffix = process.platform === 'win32' ? '.exe' : ''
        expect(names.has(`nvpair-codex-worker${suffix}`)).toBe(true)
        expect(names.has(`nvpair-codex-supervisor${suffix}`)).toBe(true)
    })
})
