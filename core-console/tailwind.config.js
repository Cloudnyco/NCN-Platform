/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  theme: {
    // Gray scale is overridden to be CSS-var-driven so the whole palette
    // flips polarity in light mode without templates needing dark: prefixes.
    // See src/style.css for the var definitions.
    colors: ({ colors }) => ({
      ...colors,
      gray: {
        50:  'rgb(var(--g-50)  / <alpha-value>)',
        100: 'rgb(var(--g-100) / <alpha-value>)',
        200: 'rgb(var(--g-200) / <alpha-value>)',
        300: 'rgb(var(--g-300) / <alpha-value>)',
        400: 'rgb(var(--g-400) / <alpha-value>)',
        500: 'rgb(var(--g-500) / <alpha-value>)',
        600: 'rgb(var(--g-600) / <alpha-value>)',
        700: 'rgb(var(--g-700) / <alpha-value>)',
        800: 'rgb(var(--g-800) / <alpha-value>)',
        900: 'rgb(var(--g-900) / <alpha-value>)',
        950: 'rgb(var(--g-950) / <alpha-value>)'
      }
    }),
    // Industrial override: kill all soft radii. Sharp edges only.
    borderRadius: {
      none: '0px',
      DEFAULT: '0px',
      sm: '0px',
      md: '0px',
      lg: '0px',
      xl: '0px',
      '2xl': '0px',
      '3xl': '0px',
      full: '0px'
    },
    extend: {
      fontFamily: {
        mono: [
          'JetBrains Mono',
          'Fira Code',
          'IBM Plex Mono',
          // CJK fallback chain — browsers will pick whichever matches the
          // glyph being rendered. Latin → JetBrains Mono; Chinese → Noto Sans.
          'Noto Sans SC',
          'Noto Sans TC',
          'PingFang SC',
          'PingFang TC',
          'Microsoft YaHei',
          'Microsoft JhengHei',
          'Menlo',
          'Consolas',
          'ui-monospace',
          'SFMono-Regular',
          'monospace'
        ]
      },
      colors: {
        // Operator accent (翠绿)
        accent: {
          DEFAULT: '#10b981', // emerald-500
          dim: '#047857'      // emerald-700
        }
      },
      keyframes: {
        'pulse-noc': {
          '0%, 100%': { opacity: '1', boxShadow: '0 0 0 0 rgba(16,185,129,0.7)' },
          '50%':      { opacity: '0.55', boxShadow: '0 0 0 6px rgba(16,185,129,0)' }
        },
        'gradient-x': {
          '0%, 100%': { 'background-position': '0% 50%' },
          '50%':      { 'background-position': '100% 50%' }
        },
        'float-a': {
          '0%, 100%': { transform: 'translate3d(0,0,0)' },
          '50%':      { transform: 'translate3d(40px,-30px,0)' }
        },
        'float-b': {
          '0%, 100%': { transform: 'translate3d(0,0,0)' },
          '50%':      { transform: 'translate3d(-50px,40px,0)' }
        },
        'float-c': {
          '0%, 100%': { transform: 'translate3d(0,0,0)' },
          '50%':      { transform: 'translate3d(30px,30px,0)' }
        },
        'scanline': {
          '0%, 100%': { transform: 'translateY(-100%)' },
          '50%':      { transform: 'translateY(100vh)' }
        },
        // Diagonal white-light sweep across the hero — lain.sh signature.
        'scan-sweep': {
          '0%':   { transform: 'translateX(-150%)', opacity: '0' },
          '15%':  { opacity: '0.18' },
          '50%':  { opacity: '0.35' },
          '85%':  { opacity: '0.10' },
          '100%': { transform: 'translateX(150%)', opacity: '0' }
        },
        // Code-cloud row drift, left → right, used with custom durations per row.
        'code-flow': {
          from: { transform: 'translateX(-90%)' },
          to:   { transform: 'translateX(90%)' }
        },
        // Grid background-position drift (the dotted grid layer slowly pans).
        'grid-drift': {
          from: { backgroundPosition: '0 0' },
          to:   { backgroundPosition: '52px 52px' }
        },
        // Soft "breathing" on the gradient headline. Originally an animated
        // `filter: drop-shadow(...)`, but that re-rasterizes the headline
        // every frame on mobile GPUs. Replaced with pure opacity pulsing
        // on a static text-shadow set in CSS (style.css).
        'glow-pulse': {
          '0%, 100%': { opacity: '0.92' },
          '50%':      { opacity: '1' }
        },
        // Occasional RGB chromatic aberration on the logo — 92% normal, ~8% glitching.
        'glitch-rgb': {
          '0%,92%,100%': { textShadow: 'none', transform: 'translate3d(0,0,0)' },
          '93%':         { textShadow: '2px 0 #ec4899, -2px 0 #10b981', transform: 'translate3d(-1px,0,0)' },
          '94%':         { textShadow: '-2px 0 #3b82f6, 2px 0 #ec4899', transform: 'translate3d(2px,0,0)' },
          '95%':         { textShadow: '2px 0 #10b981, -2px 0 #3b82f6', transform: 'translate3d(-1px,1px,0)' },
          '96%':         { textShadow: 'none', transform: 'translate3d(1px,-1px,0)' }
        }
      },
      animation: {
        'pulse-noc':  'pulse-noc 1.4s cubic-bezier(0.4,0,0.6,1) infinite',
        'gradient-x': 'gradient-x 8s ease infinite',
        'float-a':    'float-a 14s ease-in-out infinite',
        'float-b':    'float-b 18s ease-in-out infinite',
        'float-c':    'float-c 22s ease-in-out infinite',
        'scanline':   'scanline 6s linear infinite',
        'scan-sweep': 'scan-sweep 13s cubic-bezier(0.4,0,0.6,1) infinite',
        'code-flow':  'code-flow 24s linear infinite',
        'grid-drift': 'grid-drift 24s linear infinite',
        'glow-pulse': 'glow-pulse 3.6s ease-in-out infinite alternate',
        'glitch-rgb': 'glitch-rgb 7s steps(1,end) infinite'
      }
    }
  },
  plugins: []
}
