// FilesPanelIcon 是右侧文件栏的开关图标：右侧栏展开时箭头朝右，收起时朝左。
export default function FilesPanelIcon({ open }: { open: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="M15 4v16" />
      {open ? <path d="m10 10 2 2-2 2" /> : <path d="m12 10-2 2 2 2" />}
    </svg>
  )
}
