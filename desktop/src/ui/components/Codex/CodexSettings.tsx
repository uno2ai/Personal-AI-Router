// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react'
import { Badge, Button, Flex, Stack, Text } from '@nvidia/foundations-react-core'

import { useCodexStore } from '@/ui/stores/codex.store'
import type { CodexPolicyCeiling } from '@/shared/types/codex'
import CodexTasks from './CodexTasks'

const STATUS_LABELS: Record<string, string> = {
    disabled: 'Disabled',
    setup_required: 'Setup required',
    starting: 'Starting',
    ready: 'Ready',
    busy: 'Busy',
    unauthorized: 'Unauthorized',
    incompatible: 'Incompatible',
    failed: 'Failed',
    stopped: 'Stopped'
}

const REGISTRATION_LABELS: Record<string, string> = {
    unregistered: 'Unregistered',
    registered: 'Registered',
    waiting_for_main: 'Waiting for Main Codex',
    connected: 'Connected',
    failed: 'Failed'
}

export default function CodexSettings() {
    const { state, loading, error, refresh, configureWorker, setWorkerEnabled, applyRegistration, removeRegistration } =
        useCodexStore()
    const [workspaceRoot, setWorkspaceRoot] = useState('')
    const [codexExecutable, setCodexExecutable] = useState('')
    const [policyCeiling, setPolicyCeiling] = useState<CodexPolicyCeiling>('read-only')

    useEffect(() => {
        void refresh()
    }, [refresh])

    const worker = state?.worker
    const registration = state?.registration

    useEffect(() => {
        if (worker) setPolicyCeiling(worker.policyCeiling)
    }, [worker])

    return (
        <Stack gap="6" className="relative py-8 px-3 w-full">
            <Stack gap="2">
                <Text kind="body/bold/lg">Codex Pair</Text>
                <Text kind="body/regular/sm" className="text-subtle-color">
                    PAIR manages the local Worker and Main Codex MCP registration. Task prompts and
                    responses stay outside the PAIR UI.
                </Text>
            </Stack>

            {error && <div className="text-red-400 text-sm">{error}</div>}

            <div className="settings-card pair-paper p-4">
                <Stack gap="4">
                    <Flex align="center" justify="between" gap="3">
                        <Text kind="body/semibold/md">Local Worker</Text>
                        <Badge color={worker?.state === 'ready' || worker?.state === 'busy' ? 'green' : 'gray'} kind="solid">
                            {STATUS_LABELS[worker?.state ?? 'disabled'] ?? 'Unknown'}
                        </Badge>
                    </Flex>
                    <label className="flex flex-col gap-1 text-sm">
                        Workspace root
                        <input
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={workspaceRoot}
                            onChange={event => setWorkspaceRoot(event.target.value)}
                            placeholder="/absolute/path/to/workspace"
                        />
                    </label>
                    <label className="flex flex-col gap-1 text-sm">
                        Codex executable (optional)
                        <input
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={codexExecutable}
                            onChange={event => setCodexExecutable(event.target.value)}
                            placeholder="/absolute/path/to/codex"
                        />
                    </label>
                    <label className="flex flex-col gap-1 text-sm">
                        Policy ceiling
                        <select
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={policyCeiling}
                            onChange={event => setPolicyCeiling(event.target.value as CodexPolicyCeiling)}
                        >
                            <option value="read-only">Read-only</option>
                            <option value="workspace-write">Workspace write</option>
                        </select>
                    </label>
                    <Flex gap="2" wrap="wrap">
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={loading || !workspaceRoot}
                            onClick={() =>
                                void configureWorker({
                                    workspaceRoot,
                                    codexExecutable: codexExecutable || undefined,
                                    policyCeiling
                                })
                            }
                        >
                            Save worker settings
                        </Button>
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={loading || !worker || worker.state === 'setup_required'}
                            onClick={() => void setWorkerEnabled(!worker?.enabled)}
                        >
                            {worker?.enabled ? 'Disable worker' : 'Enable worker'}
                        </Button>
                        <Button kind="secondary" size="small" disabled={loading} onClick={() => void refresh()}>
                            Refresh
                        </Button>
                    </Flex>
                    {worker?.error && <Text kind="body/regular/sm">{worker.error}</Text>}
                </Stack>
            </div>

            <div className="settings-card pair-paper p-4">
                <Stack gap="4">
                    <Flex align="center" justify="between" gap="3">
                        <Text kind="body/semibold/md">Main Codex MCP</Text>
                        <Badge color={registration?.state === 'connected' ? 'green' : 'gray'} kind="solid">
                            {REGISTRATION_LABELS[registration?.state ?? 'unregistered'] ?? 'Unknown'}
                        </Badge>
                    </Flex>
                    <Text kind="body/regular/sm" className="text-subtle-color">
                        A successful file write means registered; Main Codex must reload its configuration
                        before tools are connected.
                    </Text>
                    <Flex gap="2" wrap="wrap">
                        <Button kind="secondary" size="small" disabled={loading} onClick={() => void applyRegistration()}>
                            Apply registration
                        </Button>
                        <Button kind="secondary" size="small" disabled={loading} onClick={() => void removeRegistration()}>
                            Remove PAIR entry
                        </Button>
                    </Flex>
                    {registration?.path && <Text kind="body/regular/xs">Config: {registration.path}</Text>}
                </Stack>
            </div>

            <CodexTasks />
        </Stack>
    )
}
