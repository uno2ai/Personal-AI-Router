import { describe, expect, it } from 'vitest'

import { isAllowedRendererNavigation } from '@/electron/ipc/safe-handle'

describe('privileged renderer origin checks', () => {
    it('requires the exact configured origin and top-level frame', () => {
        expect(
            isAllowedRendererNavigation('http://localhost:5173/', 'http://localhost:5173', '', true)
        ).toBe(true)
        expect(
            isAllowedRendererNavigation('http://localhost:5173/evil', 'http://localhost:5173', '', true)
        ).toBe(true)
        expect(
            isAllowedRendererNavigation('http://attacker.test/', 'http://localhost:5173', '', true)
        ).toBe(false)
        expect(
            isAllowedRendererNavigation('http://localhost:5173/', 'http://localhost:5173', '', false)
        ).toBe(false)
    })

    it('fails closed when no origin is configured and only accepts the packaged file', () => {
        const file = 'file:///Applications/PAIR.app/Contents/Resources/app.asar/ui/index.html'
        expect(isAllowedRendererNavigation(file, '', file, true)).toBe(true)
        expect(isAllowedRendererNavigation(`${file}?window=tray`, '', file, true)).toBe(true)
        expect(isAllowedRendererNavigation('file:///tmp/evil.html', '', file, true)).toBe(false)
        expect(isAllowedRendererNavigation('file:///tmp/evil.html', '', '', true)).toBe(false)
    })
})
