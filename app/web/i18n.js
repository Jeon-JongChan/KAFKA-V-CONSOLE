// 다국어: 언어 파일 등록, 현재 언어(localStorage 저장), 번역 함수 t().
import en from './locales/en.js'
import ko from './locales/ko.js'
const { reactive } = Vue

/** 지원 언어. 새 언어는 locales/xx.js 를 만들고 여기에 한 줄 추가 */
export const LOCALES = { en, ko }

/** 현재 언어. 저장된 선택 → 브라우저 언어 → 영어 순 */
export const i18n = reactive({
  lang: localStorage.getItem('kvc.lang') || (LOCALES[navigator.language.slice(0, 2)] ? navigator.language.slice(0, 2) : 'en'),
})

/** 언어 변경 + 저장. v-model="i18n.lang" 으로 바로 바인딩해도 동작하게 watch 대신 setter 에서 저장한다. */
export function setLang(lang) {
  if (!LOCALES[lang]) return
  i18n.lang = lang
  localStorage.setItem('kvc.lang', lang)
  document.documentElement.lang = lang
}
document.documentElement.lang = i18n.lang

/**
 * 키를 현재 언어 문자열로 바꾼다. 현재 언어에 없으면 영어, 그것도 없으면 키 자체.
 * params 의 값으로 {name} 자리표시자를 치환한다. i18n.lang 을 읽으므로 화면은 언어 변경 시 자동 갱신된다.
 */
export function t(key, params) {
  const s = LOCALES[i18n.lang]?.[key] ?? en[key] ?? key
  return params ? s.replace(/\{(\w+)\}/g, (_, k) => params[k] ?? '') : s
}
