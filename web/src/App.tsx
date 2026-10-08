import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { ToastProvider } from './components/ToastProvider'
import AppShell from './components/AppShell'
import LoginPage from './pages/LoginPage'
import AccountsPage from './pages/AccountsPage'
import AliasesPage from './pages/AliasesPage'
import InboxPage from './pages/InboxPage'
import HelpPage from './pages/HelpPage'
import SharedMailboxPage from './pages/SharedMailboxPage'

function ProtectedLayout() {
  const { status } = useAuth()
  if (status === 'checking') {
    return <p className="empty-state" aria-busy="true">加载中…</p>
  }
  if (status === 'anonymous') {
    return <Navigate to="/login" replace />
  }
  return <AppShell />
}

function AdminRoutes() {
  return <AuthProvider><Routes>
    <Route path="/login" element={<LoginPage />} />
    <Route element={<ProtectedLayout />}>
      <Route path="/accounts" element={<AccountsPage />} />
      <Route path="/aliases" element={<AliasesPage />} />
      <Route path="/inbox" element={<InboxPage />} />
      <Route path="/help" element={<HelpPage />} />
      <Route path="*" element={<Navigate to="/inbox" replace />} />
    </Route>
  </Routes></AuthProvider>
}

export default function App() {
  useEffect(() => {
    try {
      const theme = localStorage.getItem('icloud-mail-theme')
      if (theme === 'light' || theme === 'dark') document.documentElement.dataset.theme = theme
      else delete document.documentElement.dataset.theme
    } catch { /* System appearance is the default when preferences cannot be read. */ }
  }, [])

  return (
    <BrowserRouter>
        <ToastProvider>
          <Routes>
            <Route path="/share" element={<SharedMailboxPage />} />
            <Route path="*" element={<AdminRoutes />} />
          </Routes>
        </ToastProvider>
    </BrowserRouter>
  )
}
