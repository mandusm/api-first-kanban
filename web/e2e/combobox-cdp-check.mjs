// Combobox live check: filter behaviour, keyboard navigation, never-free-form,
// and the modal's Escape layering, against the real app + API over CDP.
//   APP_URL=http://127.0.0.1:5311/ node web/e2e/combobox-cdp-check.mjs
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const APP = process.env.APP_URL || 'http://127.0.0.1:5311/'
const { send, onMessage } = await connectCdpPage({ url: APP })
const { js, waitJs } = makeEval(send)

const exceptions = []
onMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') {
    exceptions.push(m.params.exceptionDetails?.exception?.description ?? JSON.stringify(m.params))
  }
})

const failures = []
function check(name, cond, detail = '') {
  if (cond) console.log('PASS', name)
  else {
    console.log('FAIL', name, detail)
    failures.push(name)
  }
}

const HELPERS = `
window.__c = {
  titleButtons: () => Array.from(document.querySelectorAll('article[data-item] h3 button')),
  openCard: (title) => {
    const b = window.__c.titleButtons().find((x) => x.textContent.trim() === title)
    if (!b) return false
    b.click()
    return true
  },
  dlg: () => document.querySelector('dialog[open]'),
  input: (label) => (window.__c.dlg() || document).querySelector('input[aria-label="' + label + '"]'),
  listbox: (label) => {
    const i = window.__c.input(label)
    if (!i) return null
    const id = i.getAttribute('aria-controls')
    return id ? document.getElementById(id) : (window.__c.dlg() || document).querySelector('[role="listbox"]')
  },
  optionLabels: (label) => {
    const lb = window.__c.listbox(label)
    if (!lb) return null
    return Array.from(lb.querySelectorAll('[role="option"]')).map((o) => o.textContent.trim())
  },
  emptyText: (label) => {
    const lb = window.__c.listbox(label)
    return lb ? (lb.textContent.trim() === 'No matches' ? 'No matches' : '') : null
  },
  type: (label, value) => {
    const i = window.__c.input(label)
    if (!i) return false
    i.focus()
    i.value = value
    i.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  },
  key: (label, key) => {
    const i = window.__c.input(label)
    if (!i) return false
    i.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
    return true
  },
  inputValue: (label) => (window.__c.input(label) || {}).value ?? null,
  activeDesc: (label) => (window.__c.input(label) || {}).getAttribute?.('aria-activedescendant') ?? null,
  addDisabled: () => {
    const b = Array.from((window.__c.dlg() || document).querySelectorAll('button')).find((x) => x.textContent.trim() === 'Add')
    return b ? b.disabled : null
  },
  addClick: () => {
    const b = Array.from((window.__c.dlg() || document).querySelectorAll('button')).find((x) => x.textContent.trim() === 'Add')
    if (b) b.click()
    return !!b
  },
  relationCount: () => {
    const d = window.__c.dlg()
    if (!d) return null
    return d.querySelectorAll('ul > li').length
  },
  dropdownOpen: (label) => !!window.__c.listbox(label),
};
return true`

await js(HELPERS)
check('board renders cards', await waitJs('return document.querySelectorAll("article[data-item]").length > 0'))

// --- Column create-form parent combobox (third consumer) -------------------
await js('const b = Array.from(document.querySelectorAll("button")).find((x) => x.textContent.trim() === "+ Add item"); if (b) b.click(); return !!b')
check('column create-form parent combobox present', await waitJs('return !!document.querySelector(\'input[aria-label="Parent"]\')'))
await js('const i = document.querySelector(\'input[aria-label="Parent"]\'); i.focus(); i.value = "milestone"; i.dispatchEvent(new Event("input", { bubbles: true })); return true')
const colOpts = await js('const i = document.querySelector(\'input[aria-label="Parent"]\'); const lb = document.getElementById(i.getAttribute("aria-controls")); return lb ? Array.from(lb.querySelectorAll("[role=option]")).map((o) => o.textContent.trim()) : null')
check(
  'column parent combobox filters by label',
  Array.isArray(colOpts) && colOpts.length > 0 && colOpts.every((l) => l.toLowerCase().includes('milestone')),
  JSON.stringify(colOpts)
)
await js('const b = Array.from(document.querySelectorAll("button")).find((x) => x.textContent.trim() === "Cancel"); if (b) b.click(); return true')

// --- open an item whose target list contains the DRY item ------------------
check('open card "Typable Select fields"', await js('return window.__c.openCard("Typable Select fields")'))
check('modal is open', await waitJs('return !!window.__c.dlg()'))
check('relation target combobox present', await waitJs('return !!window.__c.input("Related item")'))

// --- parent combobox: filter by kind label ---------------------------------
check('parent combobox present', await waitJs('return !!window.__c.input("Parent")'))
await js('return window.__c.type("Parent", "epic")')
const parentEpic = await js('return window.__c.optionLabels("Parent")')
check(
  'parent filter "epic" keeps only epic labels',
  Array.isArray(parentEpic) && parentEpic.length > 0 && parentEpic.every((l) => l.toLowerCase().includes('epic')),
  JSON.stringify(parentEpic)
)
await js('return window.__c.type("Parent", "zzzz-nope")')
check('parent zero matches shows "No matches"', (await js('return window.__c.emptyText("Parent")')) === 'No matches')
await js('return window.__c.key("Parent", "Escape")')

// --- relation target combobox: the DRY example -----------------------------
await js('return window.__c.type("Related item", "dry")')
const dryOptions = await js('return window.__c.optionLabels("Related item")')
check(
  'relation filter "dry" is case-insensitive substring',
  Array.isArray(dryOptions) &&
    dryOptions.length > 0 &&
    dryOptions.every((l) => l.toLowerCase().includes('dry')) &&
    dryOptions.some((l) => l.includes('DRY AND SOLID')),
  JSON.stringify(dryOptions)
)

// empty query lists many options (all vs filtered)
await js('return window.__c.type("Related item", "")')
const allOptions = await js('return window.__c.optionLabels("Related item")')
check('empty query lists all options (>1)', Array.isArray(allOptions) && allOptions.length > 1, String(allOptions?.length))

// --- keyboard nav ----------------------------------------------------------
await js('return window.__c.type("Related item", "dry")')
await js('return window.__c.key("Related item", "ArrowDown")')
const firstActive = await js('return window.__c.activeDesc("Related item")')
check('ArrowDown activates an option', typeof firstActive === 'string' && firstActive.length > 0, String(firstActive))
const before = await js('return window.__c.addDisabled()')
await js('return window.__c.key("Related item", "Enter")')
const afterValue = await js('return window.__c.inputValue("Related item")')
const stillOpen = await js('return window.__c.dropdownOpen("Related item")')
const addDisabled = await js('return window.__c.addDisabled()')
check('Enter commits the active option into the field', typeof afterValue === 'string' && afterValue.toLowerCase().includes('dry'), String(afterValue))
check('Enter closes the dropdown', stillOpen === false)
// A submit would run addRelation and reset the field to '' (Add disabled); a
// selection keeps the chosen value and leaves Add enabled.
check('Enter does NOT submit the relation form', addDisabled === false, `addDisabled=${addDisabled} before=${before}`)

// --- Escape layering -------------------------------------------------------
await js('return window.__c.type("Related item", "dry")')
check('dropdown open before Escape', (await js('return window.__c.dropdownOpen("Related item")')) === true)
await js('return window.__c.key("Related item", "Escape")')
check('first Escape closes only the dropdown', (await js('return window.__c.dropdownOpen("Related item")')) === false && (await js('return !!window.__c.dlg()')) === true)

// --- never free-form -------------------------------------------------------
await js('return window.__c.type("Related item", "totally free text")')
await js('window.__c.input("Related item").dispatchEvent(new FocusEvent("blur")); return true')
const reverted = await js('return window.__c.inputValue("Related item")')
check('blur reverts free-form text to the committed option label', reverted !== 'totally free text', String(reverted))
await js('return window.__c.type("Related item", "")')
const labels = await js('return window.__c.optionLabels("Related item")')
check('reverted field text is a real option label (never free-form)', Array.isArray(labels) && labels.includes(reverted), `${reverted} in ${JSON.stringify(labels)}`)

check('no page exceptions', exceptions.length === 0, JSON.stringify(exceptions))

console.log(failures.length === 0 ? '\nALL CHECKS PASSED' : `\n${failures.length} CHECK(S) FAILED`)
process.exit(failures.length === 0 ? 0 : 1)
