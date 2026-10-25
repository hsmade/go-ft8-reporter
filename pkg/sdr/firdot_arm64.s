//go:build arm64 && !purego

#include "textflag.h"

// func firDot(in []complex64, taps []float32) complex64
TEXT ·firDot(SB), NOSPLIT, $0-56
 MOVD in_base+0(FP), R0
 MOVD taps_base+24(FP), R1
 MOVD taps_len+32(FP), R2
 VEOR V0.B16, V0.B16, V0.B16
 VEOR V1.B16, V1.B16, V1.B16
 CMP $4, R2
 BLT reduce
vector:
 VLD1.P 16(R1), [V2.S4]
 VLD2.P 32(R0), [V3.S4, V4.S4]
 VFMLA V2.S4, V3.S4, V0.S4
 VFMLA V2.S4, V4.S4, V1.S4
 SUB $4, R2
 CMP $4, R2
 BGE vector
reduce:
 FMOVS F0, F5
 FMOVS F1, F6
 VMOV V0.S[1], R3
 FMOVS R3, F2
 FADDS F2, F5, F5
 VMOV V0.S[2], R3
 FMOVS R3, F2
 FADDS F2, F5, F5
 VMOV V0.S[3], R3
 FMOVS R3, F2
 FADDS F2, F5, F5
 VMOV V1.S[1], R3
 FMOVS R3, F2
 FADDS F2, F6, F6
 VMOV V1.S[2], R3
 FMOVS R3, F2
 FADDS F2, F6, F6
 VMOV V1.S[3], R3
 FMOVS R3, F2
 FADDS F2, F6, F6
 FMOVS F5, F0
 FMOVS F6, F1
 CBZ R2, done
scalar:
 FMOVS (R1), F2
 FMOVS (R0), F3
 FMOVS 4(R0), F4
 FMULS F2, F3, F3
 FMULS F2, F4, F4
 FADDS F3, F0, F0
 FADDS F4, F1, F1
 ADD $4, R1
 ADD $8, R0
 SUB $1, R2
 CBNZ R2, scalar
done:
 FMOVS F0, ret_real+48(FP)
 FMOVS F1, ret_imag+52(FP)
 RET
