package utils

// 提供字符串递增功能
type AdvancedStringIncrementer struct {
	characterSet   []rune
	charToIndexMap map[rune]int
}

// 创建新的字符串递增器
func NewAdvancedStringIncrementer() *AdvancedStringIncrementer {
	charset := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	charsetRunes := []rune(charset)

	charMap := make(map[rune]int)
	for i, char := range charsetRunes {
		charMap[char] = i
	}

	return &AdvancedStringIncrementer{
		characterSet:   charsetRunes,
		charToIndexMap: charMap,
	}
}

// 递增字符串，确保返回结果不等于输入
func (asi *AdvancedStringIncrementer) IncrementString(input string) string {
	if input == "" {
		// 空字符串时返回字符集的第一个字符
		return string(asi.characterSet[0])
	}

	// 直接将字符串转为rune数组处理
	runes := []rune(input)
	base := len(asi.characterSet)

	// 从最后一位开始递增
	for i := len(runes) - 1; i >= 0; i-- {
		char := runes[i]
		if index, exists := asi.charToIndexMap[char]; exists {
			// 当前字符在字符集中
			if index < base-1 {
				// 不需要进位，直接递增当前字符
				runes[i] = asi.characterSet[index+1]
				return string(runes)
			} else {
				// 需要进位，当前字符重置为第一个字符
				runes[i] = asi.characterSet[0]
				// 继续处理前一位
			}
		} else {
			// 无效字符，替换为字符集的第一个字符
			runes[i] = asi.characterSet[0]
			return string(runes)
		}
	}

	// 所有位都进位了，在最前面添加字符集的第一个字符
	return string(asi.characterSet[0]) + string(runes)
}
