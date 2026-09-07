// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

const uiRoot = path.resolve(__dirname, '../../src/ui/components/Codex')

describe('Codex UI data boundary', () => {
    it('renders worker policy and metadata task controls through the existing settings surface', () => {
        const settings = fs.readFileSync(path.join(uiRoot, 'CodexSettings.tsx'), 'utf8')
        const tasks = fs.readFileSync(path.join(uiRoot, 'CodexTasks.tsx'), 'utf8')
        expect(settings).toContain('CodexTasks')
        expect(settings).toContain('policyCeiling')
        expect(tasks).toContain('task.taskId')
        expect(tasks).toContain('task.attemptId')
        expect(tasks).toContain('task.leaseEpoch')
    })

    it('does not project prompt, response, credential, or artifact bodies into renderer rows', () => {
        const tasks = fs.readFileSync(path.join(uiRoot, 'CodexTasks.tsx'), 'utf8')
        expect(tasks).not.toMatch(/task\.(objective|prompt|response|credential|artifactBody)/)
    })
})
