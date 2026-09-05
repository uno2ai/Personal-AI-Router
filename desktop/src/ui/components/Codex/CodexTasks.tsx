import { useEffect } from 'react'
import { Button, Stack, Text } from '@nvidia/foundations-react-core'

import { useCodexStore } from '@/ui/stores/codex.store'

export default function CodexTasks() {
    const { tasks, refreshTasks, cancelTask } = useCodexStore()

    useEffect(() => {
        void refreshTasks()
        const timer = setInterval(() => void refreshTasks(), 3000)
        return () => clearInterval(timer)
    }, [refreshTasks])

    return (
        <div className="settings-card pair-paper p-4">
            <Stack gap="4">
                <Text kind="body/semibold/md">Task metadata</Text>
                {tasks.length === 0 ? (
                    <Text kind="body/regular/sm" className="text-subtle-color">
                        No task metadata is available. Main Codex must be running with the PAIR Supervisor registration.
                    </Text>
                ) : (
                    <div className="overflow-x-auto">
                        <table className="w-full text-sm">
                            <thead>
                                <tr className="text-left text-subtle-color">
                                    <th className="pr-3">Task</th>
                                    <th className="pr-3">Worker</th>
                                    <th className="pr-3">Attempt</th>
                                    <th className="pr-3">State</th>
                                    <th />
                                </tr>
                            </thead>
                            <tbody>
                                {tasks.map(task => (
                                    <tr key={task.taskId}>
                                        <td className="pr-3 py-2 font-mono">{task.taskId}</td>
                                        <td className="pr-3 py-2">{task.workerId}</td>
                                        <td className="pr-3 py-2 font-mono">{task.attemptId}</td>
                                        <td className="pr-3 py-2">{task.state}</td>
                                        <td className="py-2 text-right">
                                            {!['terminal', 'completed', 'failed', 'cancelled', 'lost'].includes(task.state) && (
                                                <Button
                                                    kind="secondary"
                                                    size="small"
                                                    onClick={() =>
                                                        void cancelTask({
                                                            taskId: task.taskId,
                                                            attemptId: task.attemptId,
                                                            leaseEpoch: task.leaseEpoch
                                                        })
                                                    }
                                                >
                                                    Cancel
                                                </Button>
                                            )}
                                        </td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    </div>
                )}
            </Stack>
        </div>
    )
}
