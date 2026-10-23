import { create } from 'zustand'

export type BackendStatus = 'idle' | 'ready' | 'error'

type AppState = {
  status: BackendStatus
  setStatus: (status: BackendStatus) => void
}

export const useAppStore = create<AppState>((set) => ({
  status: 'idle',
  setStatus: (status) => set({ status }),
}))
