import { ipcRenderer } from 'electron'

import type {
    CodexDesktopState,
    CodexRegistrationSnapshot,
    CodexTaskMetadata,
    CodexTaskReference,
    CodexWorkerConfigInput
} from '@/shared/types/codex'
import { invokeAndUnwrap } from './unwrap'

export interface ICodexApi {
    getState(): Promise<CodexDesktopState>
    configureWorker(input: CodexWorkerConfigInput): Promise<CodexDesktopState>
    setWorkerEnabled(enabled: boolean): Promise<CodexDesktopState>
    getMcpRegistration(): Promise<CodexRegistrationSnapshot>
    applyMcpRegistration(): Promise<CodexRegistrationSnapshot>
    removeMcpRegistration(): Promise<CodexRegistrationSnapshot>
    listTasks(): Promise<CodexTaskMetadata[]>
    cancelTask(reference: CodexTaskReference): Promise<void>
}

export const codexApi: ICodexApi = {
    getState: () => invokeAndUnwrap<CodexDesktopState>(ipcRenderer.invoke('codex:get-state')),
    configureWorker: input =>
        invokeAndUnwrap<CodexDesktopState>(ipcRenderer.invoke('codex:configure-worker', input)),
    setWorkerEnabled: enabled =>
        invokeAndUnwrap<CodexDesktopState>(
            ipcRenderer.invoke('codex:set-worker-enabled', { enabled })
        ),
    getMcpRegistration: () =>
        invokeAndUnwrap<CodexRegistrationSnapshot>(ipcRenderer.invoke('codex:get-mcp-registration')),
    applyMcpRegistration: () =>
        invokeAndUnwrap<CodexRegistrationSnapshot>(
            ipcRenderer.invoke('codex:apply-mcp-registration')
        ),
    removeMcpRegistration: () =>
        invokeAndUnwrap<CodexRegistrationSnapshot>(
            ipcRenderer.invoke('codex:remove-mcp-registration')
        ),
    listTasks: () => invokeAndUnwrap<CodexTaskMetadata[]>(ipcRenderer.invoke('codex:list-tasks')),
    cancelTask: reference =>
        invokeAndUnwrap<void>(ipcRenderer.invoke('codex:cancel-task', reference))
}
