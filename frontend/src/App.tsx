import { useEffect, useState } from 'react'
import { Greet } from '../wailsjs/go/app/App'
import { useAppStore } from './stores/app'

const statusLabel = {
  idle: '连接后端…',
  ready: '后端已连接',
  error: '后端未连接',
} as const

const statusColor = {
  idle: 'bg-amber-500',
  ready: 'bg-emerald-500',
  error: 'bg-rose-500',
} as const

export default function App() {
  const status = useAppStore((state) => state.status)
  const setStatus = useAppStore((state) => state.setStatus)
  const [message, setMessage] = useState('')

  useEffect(() => {
    let cancelled = false

    Greet('ANC')
      .then((reply) => {
        if (cancelled) return
        setMessage(reply)
        setStatus('ready')
      })
      .catch(() => {
        if (!cancelled) setStatus('error')
      })

    return () => {
      cancelled = true
    }
  }, [setStatus])

  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-6 bg-slate-950 text-slate-100">
      <h1 className="text-3xl font-semibold tracking-tight">ANC DSK</h1>
      <p className="text-sm text-slate-400">
        {message || 'Wails + React + TypeScript 脚手架已就绪'}
      </p>
      <div className="flex items-center gap-2 rounded-full border border-slate-800 px-4 py-2 text-sm">
        <span className={`size-2 rounded-full ${statusColor[status]}`} />
        {statusLabel[status]}
      </div>
    </main>
  )
}
