import fs from 'node:fs'
import path from 'node:path'

/**
 * Return an isolated userData directory for the opt-in packaged Codex E2E.
 *
 * This is deliberately a two-key gate and only accepts an existing directory
 * whose real path is below the OS temporary directory. A normal launch cannot
 * redirect PAIR state, and a symlink in the temporary tree cannot target the
 * user's real configuration.
 */
export function resolveCodexE2EUserData(
    enabled: boolean,
    candidate: string | undefined,
    temporaryRoot: string
): string | null {
    if (!enabled || !candidate) return null
    if (!path.isAbsolute(candidate)) {
        throw new Error('PAIR_CODEX_E2E_USER_DATA must be an absolute path')
    }

    const resolvedRoot = fs.realpathSync(temporaryRoot)
    const resolvedCandidate = fs.realpathSync(candidate)
    const relative = path.relative(resolvedRoot, resolvedCandidate)
    if (
        !relative ||
        relative.startsWith(`..${path.sep}`) ||
        relative === '..' ||
        path.isAbsolute(relative)
    ) {
        throw new Error('PAIR_CODEX_E2E_USER_DATA must resolve beneath the OS temporary directory')
    }
    if (!fs.statSync(resolvedCandidate).isDirectory()) {
        throw new Error('PAIR_CODEX_E2E_USER_DATA must resolve to a directory')
    }
    return resolvedCandidate
}
