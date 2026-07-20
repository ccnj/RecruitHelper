import type { CandidateCurrentPreview, CandidateProfileSelectionView } from './api'

export type CandidateWorkflowState =
  | { phase: 'idle'; preview: null; profile: null; error: '' }
  | { phase: 'reading'; preview: null; profile: null; error: '' }
  | { phase: 'preview'; preview: CandidateCurrentPreview; profile: null; error: string }
  | { phase: 'selecting'; preview: CandidateCurrentPreview; profile: null; error: '' }
  | { phase: 'selected'; preview: null; profile: CandidateProfileSelectionView; error: '' }
  | { phase: 'failed'; preview: null; profile: null; error: string }

export type CandidateWorkflowAction =
  | { type: 'accountChanged' }
  | { type: 'readStarted' }
  | { type: 'readSucceeded'; preview: CandidateCurrentPreview }
  | { type: 'readFailed'; error: string }
  | { type: 'selectStarted' }
  | { type: 'selectSucceeded'; profile: CandidateProfileSelectionView }
  | { type: 'selectFailed'; error: string }

export const initialCandidateWorkflow: CandidateWorkflowState = {
  phase: 'idle', preview: null, profile: null, error: '',
}

export function candidateWorkflowReducer(
  state: CandidateWorkflowState,
  action: CandidateWorkflowAction,
): CandidateWorkflowState {
  switch (action.type) {
    case 'accountChanged':
      return initialCandidateWorkflow
    case 'readStarted':
      return { phase: 'reading', preview: null, profile: null, error: '' }
    case 'readSucceeded':
      return { phase: 'preview', preview: action.preview, profile: null, error: '' }
    case 'readFailed':
      return { phase: 'failed', preview: null, profile: null, error: action.error }
    case 'selectStarted':
      return state.phase === 'preview' && state.preview.contactState === 'unestablished'
        ? { phase: 'selecting', preview: state.preview, profile: null, error: '' }
        : state
    case 'selectSucceeded':
      return state.phase === 'selecting'
        ? { phase: 'selected', preview: null, profile: action.profile, error: '' }
        : state
    case 'selectFailed':
      return state.phase === 'selecting'
        ? { phase: 'preview', preview: state.preview, profile: null, error: action.error }
        : state
  }
}

export function canConfirmCandidate(state: CandidateWorkflowState): boolean {
  return state.phase === 'preview' && state.preview.contactState === 'unestablished'
}
