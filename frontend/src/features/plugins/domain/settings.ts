/**
 * Plugin settings: typed options a package declares and the operator edits
 * on the Plugins page. The control plane checks declarations with the same
 * rules, in the same order; the first problem in a setting wins and is
 * reported with its path (`settings[2].options[1].value`).
 *
 * Saved values live in the plugin's storage under `setting:<key>`, a prefix
 * the plugin's own storage calls cannot reach: only the operator changes a
 * setting, the plugin reads it.
 */
export const SETTING_LIMIT = 24
export const SETTING_KEY_PATTERN = /^[a-z][A-Za-z0-9_]{0,31}$/u
export const SETTING_LABEL_LIMIT = 64
export const SETTING_DESCRIPTION_LIMIT = 200
export const SETTING_PLACEHOLDER_LIMIT = 64
export const SETTING_TEXT_LIMIT = 256
export const SETTING_OPTION_LIMIT = 32
/** Storage keys under this prefix belong to settings. */
export const SETTING_PREFIX = 'setting:'

export type SettingType = 'text' | 'number' | 'toggle' | 'select'
export const SETTING_TYPES: readonly SettingType[] = ['text', 'number', 'toggle', 'select']

interface SettingBase {
  readonly key: string
  readonly label: string
  /** Empty when the package left it out. */
  readonly description: string
}
export interface TextSetting extends SettingBase { readonly type: 'text'; readonly default: string; readonly placeholder: string; readonly maxLength: number }
export interface NumberSetting extends SettingBase { readonly type: 'number'; readonly default: number; readonly placeholder: string; readonly min?: number; readonly max?: number }
export interface ToggleSetting extends SettingBase { readonly type: 'toggle'; readonly default: boolean }
export interface SelectSetting extends SettingBase { readonly type: 'select'; readonly default: string; readonly options: readonly { readonly value: string; readonly label: string }[] }
export type PluginSetting = TextSetting | NumberSetting | ToggleSetting | SelectSetting
export type SettingValue = string | number | boolean

const FIELDS = new Set(['key', 'label', 'description', 'type', 'default', 'placeholder', 'maxLength', 'min', 'max', 'options'])
const OPTION_FIELDS = new Set(['value', 'label'])
/** Which optional attributes each type takes; any other is refused. */
const ATTRIBUTES: Readonly<Record<SettingType, readonly string[]>> = {
  text: ['placeholder', 'maxLength'],
  number: ['placeholder', 'min', 'max'],
  toggle: [],
  select: ['options'],
}
const TYPE_ONLY = ['placeholder', 'maxLength', 'min', 'max', 'options'] as const
// Go's unicode.IsControl: C0 (line breaks included), DEL and C1.
// eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/u

class SettingError extends Error {}

/** Whether value has at most limit code points (Go counts runes). */
const fits = (value: string, limit: number): boolean => value.length <= limit || [...value].length <= limit

/**
 * Checks a package's `settings` list. Appends one problem per broken entry
 * (plus list-level problems) and returns the clean list, or null when
 * anything was wrong.
 */
export function checkSettings(raw: unknown, problems: string[]): PluginSetting[] | null {
  if (!Array.isArray(raw)) {
    problems.push('"settings" must be a list.')
    return null
  }
  const before = problems.length
  if (raw.length > SETTING_LIMIT) problems.push(`"settings" has more than ${SETTING_LIMIT} entries.`)
  const settings: PluginSetting[] = []
  const keys = new Set<string>()
  raw.slice(0, SETTING_LIMIT).forEach((entry, index) => {
    try {
      const setting = checkSetting(entry, `settings[${index}]`)
      if (keys.has(setting.key)) throw new SettingError(`"settings[${index}].key" repeats "${setting.key}".`)
      keys.add(setting.key)
      settings.push(setting)
    } catch (error) {
      if (!(error instanceof SettingError)) throw error
      problems.push(error.message)
    }
  })
  return problems.length > before ? null : settings
}

function checkSetting(raw: unknown, at: string): PluginSetting {
  if (!isRecord(raw)) throw new SettingError(`"${at}" must be an object with key, label, type and default.`)
  for (const field of Object.keys(raw)) if (!FIELDS.has(field)) throw new SettingError(`Unknown field "${at}.${field}".`)
  const key = raw.key
  if (typeof key !== 'string' || !SETTING_KEY_PATTERN.test(key)) {
    throw new SettingError(`"${at}.key" must start with a lowercase letter, then up to 31 letters, digits or underscores.`)
  }
  const label = requiredText(raw.label, `${at}.label`, SETTING_LABEL_LIMIT)
  const description = optionalText(raw.description, `${at}.description`, SETTING_DESCRIPTION_LIMIT)
  const type = raw.type
  if (typeof type !== 'string' || !SETTING_TYPES.includes(type as SettingType)) throw new SettingError(`"${at}.type" must be text, number, toggle or select.`)
  const kind = type as SettingType
  for (const attribute of TYPE_ONLY) {
    if (raw[attribute] !== undefined && !ATTRIBUTES[kind].includes(attribute)) throw new SettingError(`"${at}.${attribute}" does not apply to a ${kind} setting.`)
  }
  const base = { key, label, description }
  switch (kind) {
    case 'text': {
      const maxLength = raw.maxLength === undefined ? SETTING_TEXT_LIMIT : raw.maxLength
      if (typeof maxLength !== 'number' || !Number.isInteger(maxLength) || maxLength < 1 || maxLength > SETTING_TEXT_LIMIT) {
        throw new SettingError(`"${at}.maxLength" must be a whole number from 1 to ${SETTING_TEXT_LIMIT}.`)
      }
      const placeholder = optionalText(raw.placeholder, `${at}.placeholder`, SETTING_PLACEHOLDER_LIMIT)
      need(raw, at)
      const value = textValue(raw.default, maxLength)
      if (value === null) throw new SettingError(`"${at}.default" must be text of at most ${maxLength} characters without control characters.`)
      return { ...base, type: kind, default: value, placeholder, maxLength }
    }
    case 'number': {
      const placeholder = optionalText(raw.placeholder, `${at}.placeholder`, SETTING_PLACEHOLDER_LIMIT)
      const min = finite(raw.min, `${at}.min`)
      const max = finite(raw.max, `${at}.max`)
      if (min !== undefined && max !== undefined && min > max) throw new SettingError(`"${at}.min" is larger than "${at}.max".`)
      need(raw, at)
      const range = { ...(min === undefined ? {} : { min }), ...(max === undefined ? {} : { max }) }
      const value = numberValue(raw.default, range)
      if (value === null) throw new SettingError(`"${at}.default" must be a number${rangeText(range)}.`)
      return { ...base, type: kind, default: value, placeholder, ...range }
    }
    case 'toggle':
      need(raw, at)
      if (typeof raw.default !== 'boolean') throw new SettingError(`"${at}.default" must be true or false.`)
      return { ...base, type: kind, default: raw.default }
    case 'select': {
      if (!Array.isArray(raw.options) || raw.options.length === 0 || raw.options.length > SETTING_OPTION_LIMIT) {
        throw new SettingError(`"${at}.options" must list 1 to ${SETTING_OPTION_LIMIT} choices.`)
      }
      const values = new Set<string>()
      const options = raw.options.map((option, index) => {
        const p = `${at}.options[${index}]`
        if (!isRecord(option)) throw new SettingError(`"${p}" must be an object with value and label.`)
        for (const field of Object.keys(option)) if (!OPTION_FIELDS.has(field)) throw new SettingError(`Unknown field "${p}.${field}".`)
        const value = requiredText(option.value, `${p}.value`, SETTING_LABEL_LIMIT)
        const optionLabel = requiredText(option.label, `${p}.label`, SETTING_LABEL_LIMIT)
        if (values.has(value)) throw new SettingError(`"${p}.value" repeats "${value}".`)
        values.add(value)
        return { value, label: optionLabel }
      })
      need(raw, at)
      if (typeof raw.default !== 'string' || !values.has(raw.default)) throw new SettingError(`"${at}.default" must be one of the option values.`)
      return { ...base, type: kind, default: raw.default, options }
    }
  }
}

/** The value a setting has: the saved one when it still fits the
 * declaration, otherwise the default. An upgrade that narrows a setting
 * therefore falls back to the default instead of handing the plugin a value
 * it no longer accepts. */
export function settingValue(setting: PluginSetting, stored: string | undefined): SettingValue {
  if (stored === undefined) return setting.default
  let decoded: unknown
  try {
    decoded = JSON.parse(stored)
  } catch {
    return setting.default
  }
  const accepted = acceptValue(setting, decoded)
  return accepted === null ? setting.default : accepted
}

/** Every setting's current value, by key, from the plugin's storage. */
export function settingValues(settings: readonly PluginSetting[], storage: Readonly<Record<string, string>>): Record<string, SettingValue> {
  const values: Record<string, SettingValue> = {}
  for (const setting of settings) values[setting.key] = settingValue(setting, storage[settingStorageKey(setting.key)])
  return values
}

/** The value as the declaration accepts it, or null. */
export function acceptValue(setting: PluginSetting, value: unknown): SettingValue | null {
  switch (setting.type) {
    case 'text': return textValue(value, setting.maxLength)
    case 'number': return numberValue(value, setting)
    case 'toggle': return typeof value === 'boolean' ? value : null
    case 'select': return typeof value === 'string' && setting.options.some((option) => option.value === value) ? value : null
  }
}

/** Why an edited value cannot be saved, in the operator's words, or ''. */
export function valueProblem(setting: PluginSetting, value: unknown): string {
  if (acceptValue(setting, value) !== null) return ''
  switch (setting.type) {
    case 'text': return `Use at most ${setting.maxLength} characters, without line breaks.`
    case 'number': return `Enter a number${rangeText(setting)}.`
    case 'toggle': return 'Choose on or off.'
    case 'select': return 'Choose one of the options.'
  }
}

export function settingStorageKey(key: string): string {
  return `${SETTING_PREFIX}${key}`
}

export function isSettingStorageKey(key: string): boolean {
  return key.startsWith(SETTING_PREFIX)
}

function textValue(value: unknown, maxLength: number): string | null {
  if (typeof value !== 'string' || CONTROL.test(value)) return null
  return fits(value, maxLength) ? value : null
}

function numberValue(value: unknown, range: { readonly min?: number; readonly max?: number }): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value)) return null
  if (range.min !== undefined && value < range.min) return null
  if (range.max !== undefined && value > range.max) return null
  return value
}

function rangeText(range: { readonly min?: number; readonly max?: number }): string {
  if (range.min !== undefined && range.max !== undefined) return ` from ${range.min} to ${range.max}`
  if (range.min !== undefined) return ` of at least ${range.min}`
  if (range.max !== undefined) return ` of at most ${range.max}`
  return ''
}

function need(raw: Record<string, unknown>, at: string): void {
  if (raw.default === undefined) throw new SettingError(`"${at}.default" is required.`)
}

function requiredText(value: unknown, at: string, limit: number): string {
  if (typeof value !== 'string' || !value.trim()) throw new SettingError(`"${at}" is required.`)
  if (!fits(value, limit)) throw new SettingError(`"${at}" is longer than ${limit} characters.`)
  if (CONTROL.test(value)) throw new SettingError(`"${at}" contains control characters.`)
  return value
}

function optionalText(value: unknown, at: string, limit: number): string {
  if (value === undefined) return ''
  if (typeof value !== 'string') throw new SettingError(`"${at}" must be a string.`)
  if (!fits(value, limit)) throw new SettingError(`"${at}" is longer than ${limit} characters.`)
  if (CONTROL.test(value)) throw new SettingError(`"${at}" contains control characters.`)
  return value
}

function finite(value: unknown, at: string): number | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'number' || !Number.isFinite(value)) throw new SettingError(`"${at}" must be a number.`)
  return value
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
