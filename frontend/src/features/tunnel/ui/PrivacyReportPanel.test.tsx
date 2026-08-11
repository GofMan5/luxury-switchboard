// @vitest-environment jsdom

import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { privacyWarnings, type PrivacyReport } from '../domain/privacy'
import { PrivacyReportPanel } from './PrivacyReportPanel'

const report: PrivacyReport = {
  checkedAt: '2026-08-03T18:19:07Z',
  requestUrl: 'https://example.invalid/model-tunnel/public/v1/models',
  status: 200,
  statusText: '200 OK',
  protocol: 'HTTP/2.0',
  remoteAddress: '203.0.113.10:443',
  durationMs: 42,
  bodyBytes: 96,
  headers: [{ name: 'X-Provider-Trace', values: ['visible-marker'] }],
  topLevelFields: ['data', 'object'],
  models: [{ id: 'public-model', object: 'model', created: 0, fields: ['created', 'id', 'object'] }],
  rawBody: '{"object":"list","data":[]}',
  credentialReflected: false,
  tls: {
    version: 'TLS 1.3',
    cipherSuite: 'TLS_AES_128_GCM_SHA256',
    serverName: 'example.invalid',
    certificateSubject: 'CN=example.invalid',
    certificateIssuer: 'CN=Test CA',
    certificateExpiresAt: '2027-08-03T00:00:00Z',
  },
}

describe('PrivacyReportPanel', () => {
  it('shows every response header and public model and runs the test from one button', () => {
    const onRun = vi.fn()
    render(<PrivacyReportPanel report={report} pending={false} error="" disabled={false} onRun={onRun} />)
    expect(screen.getByText('X-Provider-Trace')).toBeTruthy()
    expect(screen.getByText('visible-marker')).toBeTruthy()
    expect(screen.getByText('public-model')).toBeTruthy()
    expect(screen.getByText('Review exposed metadata')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Run privacy test' }))
    expect(onRun).toHaveBeenCalledOnce()
  })

  it('keeps ordinary transport headers out of the privacy warnings', () => {
    const safe = { ...report, headers: [{ name: 'Content-Type', values: ['application/json'] }] }
    expect(privacyWarnings(safe)).toEqual([])
  })
})
