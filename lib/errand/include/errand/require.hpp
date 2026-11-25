// errand::require: a run asks the machine for more than it was started with, and waits until it is given ( ADR 0014 ).
// The C++ side of `errand.require`, header-only, C++17.
//
//     #include <errand/require.hpp>
//
//     int main() {
//         errand::require( { .gpus = 1, .gpu_ram = "75%" } );     // C++20; in C++17, fill a Needs
//         ...                                                      // CUDA_VISIBLE_DEVICES is set
//     }
//
// Call it early, before what it protects is allocated. It only adds to the run's claim. Once granted, it sets
// CUDA_VISIBLE_DEVICES ( and the HIP / ROCR ones ) in this process, and returns the cards. Outside errand it does nothing and
// returns an empty list. Its include directory is `{errand_include}` in a runner's build line.
#pragma once

#include <cstdio>
#include <cstdlib>
#include <stdexcept>
#include <string>
#include <vector>

#if !defined( _WIN32 )
#include <sys/wait.h>
#endif

namespace errand {

/// What a run asks for. `gpu_ram` is what it takes on each of its cards: a size ( "40G" ) or a share ( "75%" ); `gpus`
/// without it takes cards whole. `ranks`: how many processes the run is. Empty or zero: not asked. That much in all, or, with
/// `extra`, that much more than the run was started with.
struct Needs {
    int         gpus      = 0;
    std::string gpu_ram   = { };
    std::string ram       = { };
    int         cpus      = 0;
    bool        exclusive = false;
    bool        extra     = false;
    int         ranks     = 0;
};

/// Thrown when what is asked cannot be given where the run is: errand runs it again, with it up front. Let it go through, or
/// exit with 75.
struct Rerun : std::runtime_error {
    using std::runtime_error::runtime_error;
};

namespace detail {
    inline std::string env( const char *name ) {
        const char *v = std::getenv( name );
        return v ? v : "";
    }

    inline void set_env( const std::string &name, const std::string &value ) {
        #if defined( _WIN32 )
        _putenv_s( name.c_str(), value.c_str() );
        #else
        setenv( name.c_str(), value.c_str(), 1 );
        #endif
    }

    inline std::string quoted( const std::string &s ) {
        #if defined( _WIN32 )
        return "\"" + s + "\"";
        #else
        std::string out = "'";
        for ( char c : s )
            out += c == '\'' ? std::string( "'\\''" ) : std::string( 1, c );
        return out + "'";
        #endif
    }

    inline std::vector<int> cards( const std::string &listed ) {
        std::vector<int> out;
        std::string num;
        for ( char c : listed + "," ) {
            if ( c == ',' ) {
                if ( ! num.empty() )
                    out.push_back( std::atoi( num.c_str() ) );
                num.clear();
            } else if ( c >= '0' && c <= '9' )
                num += c;
        }
        return out;
    }
}

/// Grow the run's claim to hold `needs` too, wait until it does, and return the cards it holds.
inline std::vector<int> require( const Needs &needs = { } ) {
    std::string cmd = detail::env( "ERRAND" );
    if ( cmd.empty() || detail::env( "ERRAND_ASKS" ).empty() )
        return { };
    cmd = detail::quoted( cmd ) + " require";
    if ( needs.gpus )
        cmd += " --gpus " + std::to_string( needs.gpus );
    if ( ! needs.gpu_ram.empty() )
        cmd += " --gpu-ram " + detail::quoted( needs.gpu_ram );
    if ( ! needs.ram.empty() )
        cmd += " --ram " + detail::quoted( needs.ram );
    if ( needs.cpus )
        cmd += " --cpus " + std::to_string( needs.cpus );
    if ( needs.ranks )
        cmd += " --ranks " + std::to_string( needs.ranks );
    if ( needs.exclusive )
        cmd += " --exclusive";
    if ( needs.extra )
        cmd += " --extra";

    std::string out;
    int status = -1;
    char buf[ 512 ];
    #if defined( _WIN32 )
    if ( FILE *pipe = _popen( ( "\"" + cmd + "\"" ).c_str(), "r" ) ) {          // ( cmd.exe takes the outer quotes off )
        while ( std::fgets( buf, sizeof buf, pipe ) )
            out += buf;
        status = _pclose( pipe );
    }
    #else
    if ( FILE *pipe = popen( cmd.c_str(), "r" ) ) {
        while ( std::fgets( buf, sizeof buf, pipe ) )
            out += buf;
        status = pclose( pipe );
        status = WIFEXITED( status ) ? WEXITSTATUS( status ) : -1;
    }
    #endif
    if ( status == 75 )
        throw Rerun( "errand::require: stopped, to run again with what it requires up front" );
    if ( status != 0 )
        throw std::runtime_error( "errand::require: `" + cmd + "` failed ( " + std::to_string( status ) + " )" );

    // NAME=value, one per line: set in this process
    std::vector<int> got;
    std::size_t start = 0;
    while ( start < out.size() ) {
        std::size_t end = out.find( '\n', start );
        std::string line = out.substr( start, end == std::string::npos ? std::string::npos : end - start );
        start = end == std::string::npos ? out.size() : end + 1;
        if ( ! line.empty() && line.back() == '\r' )
            line.pop_back();
        std::size_t eq = line.find( '=' );
        if ( eq == std::string::npos )
            continue;
        detail::set_env( line.substr( 0, eq ), line.substr( eq + 1 ) );
        if ( line.compare( 0, eq, "CUDA_VISIBLE_DEVICES" ) == 0 )
            got = detail::cards( line.substr( eq + 1 ) );
    }
    return got;
}

} // namespace errand
