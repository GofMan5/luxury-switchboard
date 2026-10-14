// @vitest-environment jsdom

import { useSyncExternalStore } from 'react'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { InsightsModel } from '../application/insights-model'
import type { InsightsPort } from '../application/insights-port'
import type { InsightsPeriod, InsightsReport, PriceCatalog } from '../domain/insights'
import { PriceEditor } from './PriceEditor'

afterEach(cleanup)

const report = (period: InsightsPeriod): InsightsReport => ({
  period, generatedAt: '', providers: [], models: [], daily: [], errors: [], unpricedModels: [],
  overview: {
    volume: { requests: 0, completed: 0, failed: 0, cancelled: 0, retries: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0, reasoningTokens: 0, totalTokens: 0, generationMs: 0, cost: 0, isPriced: false },
    successRate: 0, p50Ms: 0, p95Ms: 0, tokensPerSecond: 0, pricedRequests: 0, topErrorCode: '',
  },
})

function Editor({ model }: { model: InsightsModel }) {
  const state = useSyncExternalStore(model.subscribe, model.snapshot)
  return <>
    <button type="button" onClick={() => void model.load('48h')}>48h</button>
    <PriceEditor prices={state.prices} currency={state.pricesCurrency} phase={state.pricesPhase} knownModels={['fixture']} onSave={(draft) => model.savePrice(draft)} onRemove={(name) => model.removePrice(name)} onSetCurrency={(currency) => model.setCurrency(currency)} />
  </>
}

it('keeps a confirmed price when its editor closes and the operator changes period during save', async () => {
  let finish!: (catalog: PriceCatalog) => void
  const port: InsightsPort = {
    report: async (period) => report(period), recent: async () => ({ rows: [], available: 0 }), prices: async () => ({ prices: [], currency: 'USD' }),
    setPrice: vi.fn<InsightsPort['setPrice']>(() => new Promise((resolve) => { finish = resolve })), removePrice: async () => ({ prices: [], currency: 'USD' }), setCurrency: async () => ({ prices: [], currency: 'USD' }),
  }
  const model = new InsightsModel(port)
  await model.load('24h')
  render(<Editor model={model} />)
  fireEvent.click(screen.getByRole('button', { name: 'Prices' }))
  fireEvent.change(screen.getByPlaceholderText('gpt-6-astra'), { target: { value: 'fixture' } })
  fireEvent.change(screen.getByLabelText('Input', { exact: true }), { target: { value: '3' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save price' }))
  expect(model.snapshot().pricesPhase).toBe('saving')
  fireEvent.click(screen.getByRole('dialog').parentElement as HTMLElement)
  expect(screen.queryByRole('dialog')).toBeNull()
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: '48h' })) })
  expect((screen.getByRole('button', { name: 'Prices' }) as HTMLButtonElement).disabled).toBe(true)
  await act(async () => { finish({ prices: [{ model: 'fixture', input: 3, cachedInput: 0, output: 0, reasoning: 0, updatedAt: '' }], currency: 'USD' }) })
  fireEvent.click(screen.getByRole('button', { name: 'Prices' }))
  expect(screen.getByRole('button', { name: /fixture.*in 3/ }).textContent).toContain('fixture')
  expect(model.snapshot()).toMatchObject({ period: '48h', pricesPhase: 'ready' })
  model.dispose()
})
