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
      <AuthProvider>
        <ToastProvider>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route element={<ProtectedLayout />}>
              <Route path="/accounts" element={<AccountsPage />} />
              <Route path="/aliases" element={<AliasesPage />} />
              <Route path="/inbox" element={<InboxPage />} />
              <Route path="/help" element={<HelpPage />} />
              <Route path="*" element={<Navigate to="/inbox" replace />} />
            </Route>
          </Routes>
        </ToastProvider>
      </AuthProvider>
    </BrowserRouter>
  )
}
