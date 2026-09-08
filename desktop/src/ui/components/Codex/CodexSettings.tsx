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
    const {
        state,
        loading,
        error,
        refresh,
        configureWorker,
        setWorkerEnabled,
        applyRegistration,
        removeRegistration
    } = useCodexStore()
    const [workspaceDraft, setWorkspaceRoot] = useState<string | null>(null)
    const [executableDraft, setCodexExecutable] = useState<string | null>(null)
    const [policyDraft, setPolicyCeiling] = useState<CodexPolicyCeiling | null>(null)
    const [clusterDraft, setClusterDir] = useState<string | null>(null)
    const [listenDraft, setRemoteListen] = useState<string | null>(null)
    const [allowlistDraft, setAllowlist] = useState<string | null>(null)
    const [endpointsDraft, setEndpoints] = useState<string | null>(null)

    useEffect(() => {
        void refresh()
    }, [refresh])

    const worker = state?.worker
    const registration = state?.registration

    // Saved values populate untouched fields; refreshes preserve edits in progress.
    const workspaceRoot = workspaceDraft ?? worker?.workspaceRoot ?? ''
    const codexExecutable = executableDraft ?? worker?.codexExecutable ?? ''
    const policyCeiling = policyDraft ?? worker?.policyCeiling ?? 'read-only'
    const clusterDir = clusterDraft ?? state?.network.clusterDir ?? ''
    const remoteListen = listenDraft ?? state?.network.remoteListen ?? ''
    const allowlist = allowlistDraft ?? state?.network.supervisorAllowlist.join('\n') ?? ''
    const endpoints = endpointsDraft ?? state?.network.workerEndpoints.join('\n') ?? ''
    const lines = (value: string) =>
        value
            .split('\n')
            .map(line => line.trim())
            .filter(Boolean)

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
                        <Badge
                            color={
                                worker?.state === 'ready' || worker?.state === 'busy'
                                    ? 'green'
                                    : 'gray'
                            }
                            kind="solid"
                        >
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
                            onChange={event =>
                                setPolicyCeiling(
                                    event.target.value === 'danger-full-access'
                                        ? 'danger-full-access'
                                        : event.target.value === 'workspace-write'
                                          ? 'workspace-write'
                                          : 'read-only'
                                )
                            }
                        >
                            <option value="read-only">Read-only</option>
                            <option value="workspace-write">Workspace write</option>
                            <option value="danger-full-access">
                                YOLO — no sandbox or approvals
                            </option>
                        </select>
                    </label>
                    {policyCeiling === 'danger-full-access' && (
                        <Text kind="body/regular/sm">
                            YOLO permits tasks requested with mode=yolo to run with your full user
                            permissions, without a sandbox or approval prompts. The workspace is
                            only the starting directory, not a file-access boundary. Paired access
                            controls and task cancellation remain enabled. Apply registration and
                            reload Main Codex to make YOLO the default for new delegated tasks.
                        </Text>
                    )}
                    <Text kind="body/semibold/md">Paired remote connections</Text>
                    <Text kind="body/regular/sm">
                        Pair the computers with Add node first. For different locations, use their
                        Tailscale addresses. Remote access uses the same workspace and policy above.
                    </Text>
                    <label className="flex flex-col gap-1 text-sm">
                        PAIR cluster directory
                        <input
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={clusterDir}
                            onChange={event => setClusterDir(event.target.value)}
                            placeholder="Absolute path to the existing PAIR cluster directory"
                        />
                    </label>
                    <label className="flex flex-col gap-1 text-sm">
                        Accept remote work at (IP:port; empty disables remote access)
                        <input
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={remoteListen}
                            onChange={event => setRemoteListen(event.target.value)}
                            placeholder="100.x.x.x:14324"
                        />
                    </label>
                    <label className="flex flex-col gap-1 text-sm">
                        Allowed paired Supervisor IDs (one per line)
                        <textarea
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={allowlist}
                            onChange={event => setAllowlist(event.target.value)}
                            rows={2}
                        />
                    </label>
                    <label className="flex flex-col gap-1 text-sm">
                        Remote Workers to use (one peer-ID=https://IP:port per line)
                        <textarea
                            className="bg-transparent border border-white/20 rounded px-2 py-1"
                            value={endpoints}
                            onChange={event => setEndpoints(event.target.value)}
                            rows={2}
                        />
                    </label>
                    <Text kind="body/regular/sm">
                        Save settings, then Apply registration and reload Main Codex to use changed
                        remote Worker addresses. Leaving this page discards unsaved edits.
                    </Text>
                    <Flex gap="2" wrap="wrap">
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={loading || !workspaceRoot}
                            onClick={() =>
                                void configureWorker({
                                    workspaceRoot,
                                    codexExecutable: codexExecutable || undefined,
                                    policyCeiling,
                                    network: {
                                        clusterDir,
                                        remoteListen,
                                        supervisorAllowlist: lines(allowlist),
                                        workerEndpoints: lines(endpoints)
                                    }
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
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={loading}
                            onClick={() => void refresh()}
                        >
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
                        <Badge
                            color={registration?.state === 'connected' ? 'green' : 'gray'}
                            kind="solid"
                        >
                            {REGISTRATION_LABELS[registration?.state ?? 'unregistered'] ??
                                'Unknown'}
                        </Badge>
                    </Flex>
                    <Text kind="body/regular/sm" className="text-subtle-color">
                        A successful file write means registered; Main Codex must reload its
                        configuration before tools are connected.
                    </Text>
                    <Flex gap="2" wrap="wrap">
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={loading}
                            onClick={() => void applyRegistration()}
                        >
                            Apply registration
                        </Button>
                        <Button
                            kind="secondary"
                            size="small"
                            disabled={loading}
                            onClick={() => void removeRegistration()}
                        >
                            Remove PAIR entry
                        </Button>
                    </Flex>
                    {registration?.path && (
                        <Text kind="body/regular/xs">Config: {registration.path}</Text>
                    )}
                    {registration?.error && (
                        <Text kind="body/regular/sm">{registration.error}</Text>
                    )}
                </Stack>
            </div>

            <CodexTasks />
        </Stack>
    )
}
