import { create } from 'zustand'

import type { CodexDesktopState, CodexTaskMetadata, CodexTaskReference, CodexWorkerConfigInput } from '@/shared/types/codex'

interface CodexStore {
    state: CodexDesktopState | null
    tasks: CodexTaskMetadata[]
    loading: boolean
    error: string | null
    refresh: () => Promise<void>
    refreshTasks: () => Promise<void>
    configureWorker: (input: CodexWorkerConfigInput) => Promise<void>
    setWorkerEnabled: (enabled: boolean) => Promise<void>
    applyRegistration: () => Promise<void>
    removeRegistration: () => Promise<void>
    cancelTask: (reference: CodexTaskReference) => Promise<void>
}

export const useCodexStore = create<CodexStore>(set => ({
    state: null,
    tasks: [],
    loading: false,
    error: null,
    refresh: async () => {
        set({ loading: true, error: null })
        try {
            const state = await window.windowApi.codex.getState()
            set({ state, loading: false })
        } catch (error) {
            set({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
    },
    configureWorker: async input => {
        set({ loading: true, error: null })
        try {
            const state = await window.windowApi.codex.configureWorker(input)
            set({ state, loading: false })
        } catch (error) {
            set({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
    },
    refreshTasks: async () => {
        try {
            const tasks = await window.windowApi.codex.listTasks()
            set({ tasks, error: null })
        } catch (error) {
            set({ error: error instanceof Error ? error.message : String(error) })
        }
    },
    setWorkerEnabled: async enabled => {
        set({ loading: true, error: null })
        try {
            const state = await window.windowApi.codex.setWorkerEnabled(enabled)
            set({ state, loading: false })
        } catch (error) {
            set({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
    },
    applyRegistration: async () => {
        set({ loading: true, error: null })
        try {
            const registration = await window.windowApi.codex.applyMcpRegistration()
            set(current =>
                current.state
                    ? { state: { ...current.state, registration }, loading: false }
                    : { loading: false }
            )
        } catch (error) {
            set({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
    },
    removeRegistration: async () => {
        set({ loading: true, error: null })
        try {
            const registration = await window.windowApi.codex.removeMcpRegistration()
            set(current =>
                current.state
                    ? { state: { ...current.state, registration }, loading: false }
                    : { loading: false }
            )
        } catch (error) {
            set({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
    },
    cancelTask: async reference => {
        set({ loading: true, error: null })
        try {
            await window.windowApi.codex.cancelTask(reference)
            const tasks = await window.windowApi.codex.listTasks()
            set({ tasks, loading: false })
        } catch (error) {
            set({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
    }
}))
