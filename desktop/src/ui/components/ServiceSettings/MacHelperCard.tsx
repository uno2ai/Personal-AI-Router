// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Flex, Stack, Text } from '@nvidia/foundations-react-core'
import type { MacHelperSetupStatus } from '@/shared/types/ipc-channels'
import getErrorString from '@/shared/utils/get-error-string'
import { isElectron } from '@/ui/api/bootstrap'
import { InlineErrorBanner } from '@/ui/components/InlineErrorBanner'

export default function MacHelperCard() {
    const isMac = isElectron && window.windowApi.platform === 'MacOS'
    const [status, setStatus] = useState<MacHelperSetupStatus | null>(null)
    const [busy, setBusy] = useState<'checking' | 'configuring' | null>('checking')
    const [error, setError] = useState<string | null>(null)
    const inFlight = useRef(false)

    const updateStatus = useCallback(async (configure: boolean) => {
        if (inFlight.current) return
        inFlight.current = true
        setBusy(configure ? 'configuring' : 'checking')
        setError(null)
        try {
            setStatus(
                configure
                    ? await window.windowApi.service.setupMacHelper()
                    : await window.windowApi.service.getMacHelperStatus()
            )
        } catch (err) {
            setError(getErrorString(err))
            if (configure) {
                try {
                    setStatus(await window.windowApi.service.getMacHelperStatus())
                } catch {
                    setStatus(null)
                }
            }
        } finally {
            inFlight.current = false
            setBusy(null)
        }
    }, [])

    useEffect(() => {
        if (isMac) void updateStatus(false)
    }, [isMac, updateStatus])

    if (!isMac || status?.supported === false) return null

    const statusText =
        busy === 'checking'
            ? 'Checking status…'
            : busy === 'configuring'
              ? 'Configuring… Follow the macOS prompts.'
              : status?.complete
                ? 'Configured'
                : status?.skipped
                  ? 'Skipped at startup. You can configure it here at any time.'
                  : status
                    ? 'Not configured'
                    : 'Status unavailable'

    return (
        <div className="settings-card pair-paper p-4" aria-busy={busy !== null}>
            <Stack gap="4">
                <Text kind="body/semibold/md">Optional macOS firewall helper</Text>
                <Text kind="body/regular/sm" className="text-subtle-color">
                    The privileged helper can configure firewall access for PAIR services. It is not
                    needed just to use Codex Pair if connectivity already works. Setup requires a
                    signed NVIDIA build and may ask for administrator approval.
                </Text>
                <div role="status" aria-live="polite">
                    <Text kind="body/regular/sm">{statusText}</Text>
                </div>
                {error && <InlineErrorBanner severity="error" message={error} />}
                <Flex gap="2" wrap="wrap">
                    <Button
                        kind="secondary"
                        size="small"
                        disabled={busy !== null || !status?.supported || status.complete}
                        onClick={() => void updateStatus(true)}
                    >
                        {busy === 'configuring' ? 'Configuring…' : 'Configure'}
                    </Button>
                    {error && !status && (
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={busy !== null}
                            onClick={() => void updateStatus(false)}
                        >
                            Retry status
                        </Button>
                    )}
                </Flex>
            </Stack>
        </div>
    )
}
