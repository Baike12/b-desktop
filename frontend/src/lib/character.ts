export type CharacterId = 'lulu' | 'benben'

export const CHARACTER_STORAGE_KEY = 'b-desktop:character'

export const CHARACTERS: readonly {
  id: CharacterId
  name: string
  hint: string
}[] = [
  { id: 'lulu', name: '水豚噜噜', hint: '奶油黄点缀' },
  { id: 'benben', name: '水牛奔奔', hint: '雾灰点缀' },
]

const CHARACTER_IDS = new Set<string>(CHARACTERS.map((item) => item.id))

export function characterName(id: CharacterId): string {
  return CHARACTERS.find((item) => item.id === id)?.name ?? '水豚噜噜'
}

// readStoredCharacter 读取本机保存的角色；没有记录时沿用噜噜。
export function readStoredCharacter(): CharacterId {
  try {
    const value = window.localStorage.getItem(CHARACTER_STORAGE_KEY)
    if (value && CHARACTER_IDS.has(value)) return value as CharacterId
  } catch {
    // localStorage 不可用时退回默认角色，不影响渲染。
  }
  return 'lulu'
}

// applyCharacter 把角色写到 <html data-character>，色板由 style.css 中的变量覆盖。
export function applyCharacter(id: CharacterId): void {
  document.documentElement.dataset.character = id
}
