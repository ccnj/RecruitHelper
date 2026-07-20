import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/candidate-workflow.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/candidate-workflow.mjs',
  logLevel: 'error',
})
const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/candidate-workflow.mjs').href
const { candidateWorkflowReducer, canConfirmCandidate, initialCandidateWorkflow } = await import(moduleUrl)

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

const unestablished = {
  selectionRef: 'selection-safe', displayName: '候选人', positionTitle: '前端工程师', contactState: 'unestablished',
}
const established = { ...unestablished, selectionRef: 'selection-established', contactState: 'established' }

let state = candidateWorkflowReducer(initialCandidateWorkflow, { type: 'readStarted' })
check(state.phase === 'reading' && state.preview === null, '重新读取开始即清空旧预览')
state = candidateWorkflowReducer(state, { type: 'readSucceeded', preview: unestablished })
check(canConfirmCandidate(state), '只有明确未建联的预览可以确认')
state = candidateWorkflowReducer(state, { type: 'selectStarted' })
check(state.phase === 'selecting' && state.preview.selectionRef === 'selection-safe', '确认沿用本次内存 selectionRef')
state = candidateWorkflowReducer(state, {
  type: 'selectSucceeded', profile: { profileId: 'profile-safe', status: 'selected', created: true },
})
check(state.phase === 'selected' && state.preview === null, '建档成功后丢弃候选人预览与 selectionRef')

state = candidateWorkflowReducer(initialCandidateWorkflow, { type: 'readSucceeded', preview: established })
check(!canConfirmCandidate(state), '已有联系的候选人不能确认建档')
const blocked = candidateWorkflowReducer(state, { type: 'selectStarted' })
check(blocked === state, '非未建联状态不会进入确认请求阶段')
state = candidateWorkflowReducer(state, { type: 'accountChanged' })
check(state.phase === 'idle' && state.preview === null && state.profile === null, '切换账号清空全部候选人内存态')

console.log(fail === 0 ? '\nALL PASS' : `\n${fail} FAIL`)
process.exit(fail === 0 ? 0 : 1)
