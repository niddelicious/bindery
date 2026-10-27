import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import CoverImage from './CoverImage'

// A cover that fails to load shows the slot's placeholder, not the browser's
// broken-image icon (#3146).
describe('CoverImage', () => {
  it('shows the image while it loads', () => {
    render(<CoverImage src="/api/v1/images?url=a" alt="Fjellvinden" fallback={<span>No cover</span>} />)
    expect(screen.getByRole('img', { name: 'Fjellvinden' })).toHaveAttribute('src', '/api/v1/images?url=a')
    expect(screen.queryByText('No cover')).toBeNull()
  })

  it('shows the fallback without a src', () => {
    render(<CoverImage src="" alt="Fjellvinden" fallback={<span>No cover</span>} />)
    expect(screen.queryByRole('img')).toBeNull()
    expect(screen.getByText('No cover')).toBeInTheDocument()
  })

  it('swaps to the fallback when the image fails to load', () => {
    render(<CoverImage src="/api/v1/images?url=gone" alt="Fjellvinden" fallback={<span>No cover</span>} />)
    fireEvent.error(screen.getByRole('img', { name: 'Fjellvinden' }))
    expect(screen.queryByRole('img')).toBeNull()
    expect(screen.getByText('No cover')).toBeInTheDocument()
  })

  it('tries a new src after an earlier one failed', () => {
    const { rerender } = render(<CoverImage src="/a" alt="Fjellvinden" fallback={<span>No cover</span>} />)
    fireEvent.error(screen.getByRole('img'))
    rerender(<CoverImage src="/b" alt="Fjellvinden" fallback={<span>No cover</span>} />)
    expect(screen.getByRole('img', { name: 'Fjellvinden' })).toHaveAttribute('src', '/b')
  })

  it('renders nothing on failure when there is no fallback', () => {
    const { container } = render(<CoverImage src="/a" alt="" />)
    fireEvent.error(container.querySelector('img')!)
    expect(container.innerHTML).toBe('')
  })
})
