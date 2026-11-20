// Applies the stored theme before the first paint, so a reader who chose the
// light theme never sees the dark one flash by. This is a classic script in
// <head> for that reason: a module would run after the page is parsed.
// app.mjs runs the Dark | Light switch and writes the same key.
{
  let stored = null;
  try {
    stored = localStorage.getItem('demo-dashboard-theme');
  } catch {
    // The browser blocks storage for this page. Nothing was chosen then:
    // the default theme applies.
  }
  document.documentElement.dataset.theme = stored === 'light' ? 'light' : 'dark';
}
