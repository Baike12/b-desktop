import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { applyTheme, readStoredTheme } from './lib/theme'
import './style.css'

// 先应用主题再挂载，避免启动瞬间出现深色/浅色闪烁。
applyTheme(readStoredTheme())

const root = document.getElementById('root')
if (!root) {
  throw new Error('missing #root element')
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
